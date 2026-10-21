//go:build darwin

package cli

import "github.com/jiangxin/tiler/internal/workspace"

func init() {
	readHdiutilInfo = workspace.ReadHdiutilInfo
}
