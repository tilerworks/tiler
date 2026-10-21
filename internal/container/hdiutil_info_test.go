package container

import (
	"strings"
	"testing"
)

func TestParseHdiutilInfoReadsMountPointAndShadow(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>images</key>
	<array>
		<dict>
			<key>image-path</key>
			<string>/images/alice/app/image.dmg</string>
			<key>shadow-path</key>
			<string>/sessions/dev/runtime/m1/upper.shadow</string>
			<key>system-entities</key>
			<array>
				<dict>
					<key>dev-entry</key>
					<string>/dev/disk3</string>
				</dict>
				<dict>
					<key>mount-point</key>
					<string>/vol/a&amp;b</string>
				</dict>
			</array>
		</dict>
		<dict>
			<key>image-path</key>
			<string>/images/alice/app/image.dmg</string>
			<key>system-entities</key>
			<array>
				<dict>
					<key>dev-entry</key>
					<string>/dev/disk4</string>
				</dict>
			</array>
		</dict>
	</array>
</dict>
</plist>`
	got, err := ParseHdiutilInfo([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("volumes = %+v, want the one entity that has a mount point", got)
	}
	want := HdiutilImage{
		ImagePath:  "/images/alice/app/image.dmg",
		ShadowPath: "/sessions/dev/runtime/m1/upper.shadow",
		MountPoint: "/vol/a&b",
	}
	if got[0] != want {
		t.Fatalf("volume = %+v, want %+v", got[0], want)
	}
	// A longer path that only contains the mount point is the image
	// or the shadow, not another volume.
	if strings.Contains(got[0].ImagePath, got[0].MountPoint) {
		t.Fatal("fixture image path was the mount point")
	}
}

func TestParseHdiutilInfoRejectsEmptyAndMalformed(t *testing.T) {
	if _, err := ParseHdiutilInfo(nil); err == nil {
		t.Fatal("empty plist was accepted")
	}
	if _, err := ParseHdiutilInfo([]byte("<plist>")); err == nil {
		t.Fatal("truncated plist was accepted")
	}
	const notArray = `<plist version="1.0"><dict><key>images</key><string>nope</string></dict></plist>`
	if _, err := ParseHdiutilInfo([]byte(notArray)); err == nil {
		t.Fatal("images that are not an array were accepted")
	}
}

func TestParseHdiutilInfoWithoutImagesIsEmpty(t *testing.T) {
	const doc = `<plist version="1.0"><dict><key>framework</key><string>1</string></dict></plist>`
	got, err := ParseHdiutilInfo([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("volumes = %+v, want none", got)
	}
}
