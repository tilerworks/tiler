package image

import "github.com/jiangxin/tiler/internal/diagnostic"

type smokeMountDeniedError = diagnostic.MountDeniedError

func deniedSmokeMount(err error) *smokeMountDeniedError {
	return diagnostic.DeniedMount("build: image validation failed: FUSE smoke mount was denied.", err)
}
