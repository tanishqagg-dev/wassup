#!/bin/sh
# Writes Casks/wassup.rb for a published release and pushes it to tanishqagg-dev/homebrew-tap.
# Needs gh logged in with push access to the tap. Usage: scripts/publish-cask.sh v0.1.0
set -eu
tag="${1:?usage: scripts/publish-cask.sh vX.Y.Z}"
ver="${tag#v}"
repo=tanishqagg-dev/wassup
tap=tanishqagg-dev/homebrew-tap
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

gh release download "$tag" --repo "$repo" --pattern checksums.txt --dir "$tmp"
sum() {
  s=$(awk -v f="wassup_${ver}_$1.tar.gz" '$2 == f { print $1 }' "$tmp/checksums.txt")
  [ -n "$s" ] || { echo "checksums.txt has no wassup_${ver}_$1.tar.gz" >&2; exit 1; }
  echo "$s"
}
mac_intel=$(sum darwin_amd64)
mac_arm=$(sum darwin_arm64)
linux_intel=$(sum linux_amd64)
linux_arm=$(sum linux_arm64)
url="https://github.com/$repo/releases/download/$tag"

gh repo clone "$tap" "$tmp/tap" -- --depth 1 --quiet
mkdir -p "$tmp/tap/Casks"
cat > "$tmp/tap/Casks/wassup.rb" <<EOF
cask "wassup" do
  version "$ver"
  name "wassup"
  desc "WhatsApp from your terminal and your AI apps"
  homepage "https://github.com/$repo"

  livecheck do
    skip "Published by scripts/publish-cask.sh on each release."
  end

  binary "wassup"

  on_macos do
    on_intel do
      url "$url/wassup_${ver}_darwin_amd64.tar.gz"
      sha256 "$mac_intel"
    end
    on_arm do
      url "$url/wassup_${ver}_darwin_arm64.tar.gz"
      sha256 "$mac_arm"
    end
  end

  on_linux do
    on_intel do
      url "$url/wassup_${ver}_linux_amd64.tar.gz"
      sha256 "$linux_intel"
    end
    on_arm do
      url "$url/wassup_${ver}_linux_arm64.tar.gz"
      sha256 "$linux_arm"
    end
  end

  # The CLI binary isn't notarized; drop the quarantine bit so macOS runs it.
  postflight do
    if OS.mac?
      system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/wassup"]
    end
  end
end
EOF

cd "$tmp/tap"
git add Casks/wassup.rb
if git diff --cached --quiet; then
  echo "tap already has wassup $ver"
  exit 0
fi
git commit -q -m "wassup $ver"
git push -q
echo "published Casks/wassup.rb for $ver to $tap"
