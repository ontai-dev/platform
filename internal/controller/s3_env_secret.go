package controller

// s3_env_secret.go -- S3 credential projection for executor Jobs.
//
// The admin creates a single S3 secret (provider-specific key names accepted).
// The reconciler reads it, normalizes key names to the AWS SDK env var convention,
// and projects a per-operation Secret into the job namespace. The executor Job mounts
// it via envFrom so the Conductor binary finds S3_REGION, AWS_ACCESS_KEY_ID, etc.
//
// Cross-namespace handling: the source secret may live in seam-system while the job
// runs in seam-tenant-{cluster}. The projected secret is always in em.Namespace.
// OwnerReference on the projection lets Kubernetes GC it when the EtcdMaintenance is
// deleted.
//
// Provider key name support (platform-schema.md §10 S3 secret contract):
//   camelCase style (MinIO, Scality): accessKeyID, secretAccessKey, region, endpoint
//   AWS SDK env style:                AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, S3_REGION, S3_ENDPOINT
//
// platform-schema.md §10.

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	platformv1alpha1 "github.com/ontai-dev/platform/api/v1alpha1"
)

// s3EnvSecretSuffix is appended to the EtcdMaintenance name to form the projected
// secret name within em.Namespace.
const s3EnvSecretSuffix = "-s3-env"

// ensureS3EnvSecret reads the S3 credentials secret at sourceName/sourceNS,
// normalizes its keys to canonical AWS SDK env var names, and creates or updates
// a projected Secret in em.Namespace named {em.Name}-s3-env.
//
// The projected secret is owned by em (controller reference) so Kubernetes GCs it
// when the EtcdMaintenance CR is deleted.
//
// Returns the name of the projected secret on success.
func ensureS3EnvSecret(ctx context.Context, c client.Client, scheme *runtime.Scheme, sourceName, sourceNS string, em *platformv1alpha1.EtcdMaintenance) (string, error) {
	src := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: sourceName, Namespace: sourceNS}, src); err != nil {
		return "", fmt.Errorf("read S3 source secret %s/%s: %w", sourceNS, sourceName, err)
	}

	envData, err := NormalizeS3SecretData(src.Data)
	if err != nil {
		return "", fmt.Errorf("normalize S3 secret %s/%s: %w", sourceNS, sourceName, err)
	}

	projName := em.Name + s3EnvSecretSuffix
	proj := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projName,
			Namespace: em.Namespace,
		},
	}
	if err := controllerutil.SetControllerReference(em, proj, scheme); err != nil {
		return "", fmt.Errorf("set owner reference on S3 env secret: %w", err)
	}

	if _, err := controllerutil.CreateOrUpdate(ctx, c, proj, func() error {
		proj.Data = envData
		return nil
	}); err != nil {
		return "", fmt.Errorf("upsert S3 env secret %s/%s: %w", em.Namespace, projName, err)
	}

	return projName, nil
}

// NormalizeS3SecretData maps provider-specific secret keys to canonical AWS SDK env
// var names and returns the normalized map. Exported for unit tests.
//
// Accepted source key variants (either convention accepted for each field):
//
//	accessKeyID     | AWS_ACCESS_KEY_ID     → AWS_ACCESS_KEY_ID   (required)
//	secretAccessKey | AWS_SECRET_ACCESS_KEY → AWS_SECRET_ACCESS_KEY (required)
//	region          | S3_REGION             → S3_REGION            (required)
//	endpoint        | S3_ENDPOINT           → S3_ENDPOINT          (optional; omit for AWS native)
//
// Returns an error if any required key is absent from data.
func NormalizeS3SecretData(data map[string][]byte) (map[string][]byte, error) {
	out := make(map[string][]byte, 4)

	pick := func(dest string, candidates ...string) bool {
		for _, k := range candidates {
			if v, ok := data[k]; ok && len(v) > 0 {
				out[dest] = v
				return true
			}
		}
		return false
	}

	if !pick("AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID", "accessKeyID") {
		return nil, fmt.Errorf("S3 secret missing required key: AWS_ACCESS_KEY_ID or accessKeyID")
	}
	if !pick("AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY", "secretAccessKey") {
		return nil, fmt.Errorf("S3 secret missing required key: AWS_SECRET_ACCESS_KEY or secretAccessKey")
	}
	if !pick("S3_REGION", "S3_REGION", "region") {
		return nil, fmt.Errorf("S3 secret missing required key: S3_REGION or region")
	}
	pick("S3_ENDPOINT", "S3_ENDPOINT", "endpoint") // optional

	return out, nil
}

// appendS3EnvFrom appends an envFrom entry for envSecretName to the first container
// of the job's pod template. No-op when envSecretName is empty.
func appendS3EnvFrom(job *batchv1.Job, envSecretName string) {
	if envSecretName == "" || len(job.Spec.Template.Spec.Containers) == 0 {
		return
	}
	job.Spec.Template.Spec.Containers[0].EnvFrom = append(
		job.Spec.Template.Spec.Containers[0].EnvFrom,
		corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: envSecretName},
			},
		},
	)
}

// resolveS3CredentialsForRestore resolves S3 credentials for an etcd restore operation.
// Resolution order (mirrors backup resolution, platform-schema.md §10):
//  1. spec.s3SnapshotPath.credentialsSecretRef — per-operation override.
//  2. seam-etcd-backup-config in seam-system — cluster-wide default.
//
// Returns (secretName, secretNamespace, found, error).
func resolveS3CredentialsForRestore(ctx context.Context, c client.Client, em *platformv1alpha1.EtcdMaintenance) (string, string, bool, error) {
	if em.Spec.S3SnapshotPath != nil && em.Spec.S3SnapshotPath.CredentialsSecretRef.Name != "" {
		ref := em.Spec.S3SnapshotPath.CredentialsSecretRef
		ns := ref.Namespace
		if ns == "" {
			ns = em.Namespace
		}
		secret := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ns}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return "", "", false, nil
			}
			return "", "", false, fmt.Errorf("get restore S3 secret %s/%s: %w", ns, ref.Name, err)
		}
		return ref.Name, ns, true, nil
	}
	// Cluster-wide default.
	const defaultName = "seam-etcd-backup-config"
	const defaultNS = "seam-system"
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: defaultName, Namespace: defaultNS}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("get default S3 secret %s/%s: %w", defaultNS, defaultName, err)
	}
	return defaultName, defaultNS, true, nil
}
