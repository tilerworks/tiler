package doctor

import (
	"os"
	"strings"
)

// distro is the install-command family doctor can name. An
// unrecognized or unreadable os-release is distroUnknown, which
// prints both families.
type distro int

const (
	distroUnknown distro = iota
	distroDebian
	distroRedHat
)

// readOSRelease loads /etc/os-release. Tests replace it.
var readOSRelease = func() (string, error) {
	body, err := os.ReadFile("/etc/os-release")
	return string(body), err
}

// binaryPackage maps a probed binary to the distro package that
// ships it. apt and dnf names differ for erofsfuse.
type binaryPackage struct {
	apt string
	dnf string
}

var binaryPackages = map[string]binaryPackage{
	"mksquashfs":     {apt: "squashfs-tools", dnf: "squashfs-tools"},
	"unsquashfs":     {apt: "squashfs-tools", dnf: "squashfs-tools"},
	"squashfuse_ll":  {apt: "squashfuse", dnf: "squashfuse"},
	"mkfs.erofs":     {apt: "erofs-utils", dnf: "erofs-utils"},
	"erofsfuse":      {apt: "erofsfuse", dnf: "erofs-fuse"},
	"fuse-overlayfs": {apt: "fuse-overlayfs", dnf: "fuse-overlayfs"},
	"fusermount3":    {apt: "fuse3", dnf: "fuse3"},
	"git":            {apt: "git", dnf: "git"},
}

// withInstall appends the package command for binaries. A binary
// with no package mapping adds nothing.
func withInstall(detail string, binaries []string) string {
	hint := installHint(binaries, currentDistro())
	if hint == "" {
		return detail
	}
	if detail == "" {
		return hint
	}
	return detail + "; " + hint
}

func currentDistro() distro {
	text, err := readOSRelease()
	if err != nil {
		return distroUnknown
	}
	return classifyOS(text)
}

// classifyOS reads ID and ID_LIKE. Debian-family ids use apt.
// Red Hat-family ids use dnf. Anything else is unknown.
func classifyOS(text string) distro {
	id, like := parseOSRelease(text)
	if family := distroOf(id); family != distroUnknown {
		return family
	}
	return distroOf(like)
}

func parseOSRelease(text string) (id, like string) {
	for _, line := range strings.Split(text, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"'`)
		switch key {
		case "ID":
			id = val
		case "ID_LIKE":
			like = val
		}
	}
	return id, like
}

func distroOf(field string) distro {
	for _, tok := range strings.Fields(field) {
		switch tok {
		case "debian", "ubuntu", "linuxmint", "pop":
			return distroDebian
		case "rhel", "centos", "fedora", "rocky", "almalinux", "ol", "amzn":
			return distroRedHat
		}
	}
	return distroUnknown
}

// installHint is the install sentence for binaries, in the order
// they are listed. Repeated packages are kept once.
func installHint(binaries []string, family distro) string {
	apt, dnf := packageLists(binaries)
	if len(apt) == 0 {
		return ""
	}
	aptCmd := "sudo apt install " + strings.Join(apt, " ")
	dnfCmd := "sudo dnf install " + strings.Join(dnf, " ")
	switch family {
	case distroDebian:
		return "install: " + aptCmd
	case distroRedHat:
		return "install: " + dnfCmd
	default:
		return "install: " + aptCmd + "; " + dnfCmd
	}
}

func packageLists(binaries []string) (apt, dnf []string) {
	seenApt := map[string]bool{}
	seenDnf := map[string]bool{}
	for _, binary := range binaries {
		pkg, ok := binaryPackages[binary]
		if !ok {
			continue
		}
		if !seenApt[pkg.apt] {
			seenApt[pkg.apt] = true
			apt = append(apt, pkg.apt)
		}
		if !seenDnf[pkg.dnf] {
			seenDnf[pkg.dnf] = true
			dnf = append(dnf, pkg.dnf)
		}
	}
	return apt, dnf
}
