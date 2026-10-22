//go:build darwin

package container

func init() {
	Register(dmg{})
}
