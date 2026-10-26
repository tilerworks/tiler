package cli

import (
	"fmt"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/spf13/cobra"
)

// flagSet holds persistent flag values. A field enters the command
// line patch only when its flag Changed, so an omitted bool keeps
// the value loaded from a file.
type flagSet struct {
	imagesDir             string
	sessionsDir           string
	cacheDir              string
	keepVersions          int
	maxAge                string
	sessionMaxAge         string
	sessionAutocloseStale bool
	squashfsOptions       string
	fuseOptions           string
	cache                 bool
	logLevel              string
}

// rootPersistentFlagNames is filled by bindFlags so benchmark's
// reject list cannot drift from the root persistent set.
var rootPersistentFlagNames []string

func bindFlags(cmd *cobra.Command, flags *flagSet) {
	set := cmd.PersistentFlags()
	rootPersistentFlagNames = nil
	bindString := func(p *string, name, value, usage string) {
		set.StringVar(p, name, value, usage)
		rootPersistentFlagNames = append(rootPersistentFlagNames, name)
	}
	bindInt := func(p *int, name string, value int, usage string) {
		set.IntVar(p, name, value, usage)
		rootPersistentFlagNames = append(rootPersistentFlagNames, name)
	}
	bindBool := func(p *bool, name string, value bool, usage string) {
		set.BoolVar(p, name, value, usage)
		rootPersistentFlagNames = append(rootPersistentFlagNames, name)
	}
	bindString(&flags.imagesDir, "images-dir", "", "directory for read-only images")
	bindString(&flags.sessionsDir, "sessions-dir", "", "directory for session records and per-session runtime")
	bindString(&flags.cacheDir, "cache-dir", "", "directory for a later image cache copy")
	bindInt(&flags.keepVersions, "keep-versions", 0, "image versions to retain; zero means unlimited")
	bindString(&flags.maxAge, "max-age", "", "maximum image age; zero means unlimited")
	bindString(&flags.sessionMaxAge, "session-max-age", "", "maximum session age; zero disables age cleanup")
	bindBool(&flags.sessionAutocloseStale, "session-autoclose-stale", false, "close aged active sessions without a healthy mount")
	bindString(&flags.squashfsOptions, "squashfs-options", "", "opaque squashfs mount options")
	bindString(&flags.fuseOptions, "fuse-options", "", "opaque FUSE mount options")
	bindBool(&flags.cache, "cache", false, "copy images into the cache directory later")
	bindString(&flags.logLevel, "log-level", "", "log level: debug, info, warn, or error")
}

func commandPatch(cmd *cobra.Command, flags *flagSet) (config.Patch, error) {
	maxAge, err := changedDuration(cmd, "max-age", flags.maxAge, "max_age")
	if err != nil {
		return config.Patch{}, err
	}
	sessionMaxAge, err := changedDuration(cmd, "session-max-age", flags.sessionMaxAge, "session_max_age")
	if err != nil {
		return config.Patch{}, err
	}
	return config.Patch{
		ImagesDir:             changedString(cmd, "images-dir", &flags.imagesDir),
		SessionsDir:           changedString(cmd, "sessions-dir", &flags.sessionsDir),
		CacheDir:              changedString(cmd, "cache-dir", &flags.cacheDir),
		KeepVersions:          changedInt(cmd, "keep-versions", flags.keepVersions),
		MaxAge:                maxAge,
		SessionMaxAge:         sessionMaxAge,
		SessionAutocloseStale: changedBool(cmd, "session-autoclose-stale", flags.sessionAutocloseStale),
		SquashfsOptions:       changedString(cmd, "squashfs-options", &flags.squashfsOptions),
		FuseOptions:           changedString(cmd, "fuse-options", &flags.fuseOptions),
		CacheEnabled:          changedBool(cmd, "cache", flags.cache),
		LogLevel:              changedString(cmd, "log-level", &flags.logLevel),
	}, nil
}

func changedString(cmd *cobra.Command, name string, value *string) *string {
	if cmd.Flags().Changed(name) {
		return value
	}
	return nil
}

func changedInt(cmd *cobra.Command, name string, value int) *int {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	copied := value
	return &copied
}

func changedBool(cmd *cobra.Command, name string, value bool) *bool {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	copied := value
	return &copied
}

func changedDuration(cmd *cobra.Command, name, raw, field string) (*time.Duration, error) {
	if !cmd.Flags().Changed(name) {
		return nil, nil
	}
	parsed, err := config.ParseDuration(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return &parsed, nil
}
