package v1alpha1

// SecretRef is a reference to a Kubernetes Secret by name and namespace.
type SecretRef struct {
	// Name is the Secret name.
	Name string `json:"name"`

	// Namespace is the Secret namespace. When empty, the consuming object's
	// own namespace is used unless the schema specifies otherwise.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}
