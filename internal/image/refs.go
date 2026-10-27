package image

// Ref is one full image, and optionally one stack tip of its DAG,
// that a holder may still pin. TipID is empty for a holder that
// predates stack tips, or for a question about the full image alone
// such as a hold archive decision.
type Ref struct {
	Name   string
	FullID string
	// TipID names the stack tip the holder serves. A pin on a tip
	// covers the tip together with every ancestor, so a branch layer
	// stays protected while a holder still resolves through it.
	TipID string
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
