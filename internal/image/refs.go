package image

// Ref is one full image a holder may still pin.
type Ref struct {
	Owner  string
	Repo   string
	FullID string
}

// Refs reports whether a full image is still referenced.
type Refs interface {
	Holds(Ref) (bool, error)
}
