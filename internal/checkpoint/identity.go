package checkpoint

type restoreIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
