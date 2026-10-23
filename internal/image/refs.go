package image

// Ref is one full image a holder may still pin.
type Ref struct {
	Name   string
	FullID string
}

// Refs reports whether a full image is still referenced.
type Refs interface {
	// Holds reports whether any holder pins the image.
	Holds(Ref) (bool, error)
	// Holder names one holder pinning the image, with the same
	// answer as Holds; the name is empty when nothing pins it.
	// Refusals quote it so the user can act on the actual holder.
	Holder(Ref) (string, bool, error)
}
