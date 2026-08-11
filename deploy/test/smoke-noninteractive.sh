#!/usr/bin/env bash
#
# smoke-noninteractive.sh — verify the non-interactive install path.
#
# Runs install.sh inside an Ubuntu container with NO TTY (piped) and
# XUI_NONINTERACTIVE=1, then asserts:
#   * /etc/x-ui/install-result.env exists (mode 600) with random, non-default creds
#   * the panel reports hasDefaultCredential: false (no admin/admin remains)
#   * the panel HTTP server actually serves on the generated port/base path
#   * with a [version] argument: the installed binary reports exactly that version
#
# Requires Docker and network access (install.sh downloads the released binary).
# Usage: bash deploy/test/smoke-noninteractive.sh [version]
#   With no argument install.sh resolves releases/latest. Pass an explicit tag
#   (e.g. v3.4.2) to verify that exact release — the tag-triggered CI run does
#   this so it cannot silently validate the previous release (#5756).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
IMAGE="${SMOKE_IMAGE:-ubuntu:24.04}"
XUI_SMOKE_VERSION="${1:-}"

snell_test_fail() {
    echo "FAIL: $*" >&2
    exit 1
}

snell_file_mode() {
    stat -c %a "$1" 2> /dev/null || stat -f %Lp "$1"
}

verify_snell_readme() {
    local phrase
    for phrase in 'Linux Host' nftables Docker non-Linux v5.0.1 Surge; do
        grep -Fq "$phrase" "${REPO_ROOT}/README.md" \
            || snell_test_fail "README missing: ${phrase}"
    done
}

run_snell_helper_tests() {
    local script="$1"
    local test_root fake_bin helper_file destination actual
    test_root="$(mktemp -d)"
    fake_bin="${test_root}/bin"
    helper_file="${test_root}/snell-helpers.sh"
    destination="${test_root}/destination"
    mkdir -p "${fake_bin}"

    cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url="${!#}"
case "$url" in
    https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-amd64.zip|https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-i386.zip|https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-aarch64.zip|https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-armv7l.zip) ;;
    *) exit 90 ;;
esac
[[ "$url" != *latest* ]] || exit 91
while [[ "$#" -gt 0 ]]; do
    if [[ "$1" == "-o" ]]; then
        printf 'fake archive' > "$2"
        exit 0
    fi
    shift
done
exit 92
EOF
    cat > "${fake_bin}/sha256sum" <<'EOF'
#!/usr/bin/env bash
printf '%s  %s\n' "${FAKE_SHA256:?}" "$1"
EOF
    cat > "${fake_bin}/unzip" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
    -Z1) printf '%b\n' "${FAKE_ZIP_MEMBERS:?}" ;;
    -Z)
        [[ "${2:-}" == "-l" ]] || exit 93
        printf '%b\n' "${FAKE_ZIP_DETAILS:?}"
        ;;
    -p) printf '%s' "${FAKE_EXTRACT_CONTENT:?}" ;;
    *) exit 94 ;;
esac
EOF
    cat > "${fake_bin}/install" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${FAKE_INSTALL_FAIL:-0}" != 1 ]] || exit 95
cp "${@: -2:1}" "${@: -1}"
chmod 0755 "${@: -1}"
EOF
    chmod +x "${fake_bin}/curl" "${fake_bin}/sha256sum" "${fake_bin}/unzip" "${fake_bin}/install"

    sed -n '/^# SNELL_V5_HELPERS_BEGIN$/,/^# SNELL_V5_HELPERS_END$/p' "$script" > "$helper_file"
    # shellcheck disable=SC1090
    source "$helper_file"
    # A RETURN trap set before source would run when the helper file returns,
    # deleting the fake executables before the behavior tests use them.
    trap 'rm -rf "${test_root}"' RETURN

    local -a artifacts=(
        "amd64 https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-amd64.zip 9bea1c2b9e35b73b31634856c04d18c393072b9e5dcde6a32781d8b8f908c539"
        "386 https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-i386.zip 6a3e30928315427d6f747f26408d0f74eb88f460344d0e1fcb3f7c32c708a09d"
        "arm64 https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-aarch64.zip 2f178bf5ac468ce1a130454efa40a0603fbbe4e47ecc4880a989f4abc7f824cf"
        "armv7 https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-armv7l.zip 14489f3e857569c8835dd3598b7ea6bca5371d4290ac7cf0f6c8dfb3381c1fb2"
    )
    local artifact arch_name expected_url expected_sha
    for artifact in "${artifacts[@]}"; do
        read -r arch_name expected_url expected_sha <<< "$artifact"
        actual="$(snell_artifact_for_arch "$arch_name")" \
            || snell_test_fail "${script}: missing artifact mapping for ${arch_name}"
        [[ "$actual" == "$expected_url $expected_sha" ]] \
            || snell_test_fail "${script}: incorrect artifact mapping for ${arch_name}"
        [[ "$actual" != *latest* ]] \
            || snell_test_fail "${script}: artifact mapping must not use latest"
    done
    if snell_artifact_for_arch unsupported > /dev/null 2>&1; then
        snell_test_fail "${script}: unsupported architecture was accepted"
    fi

    PATH="${fake_bin}:$PATH" \
    FAKE_SHA256="9bea1c2b9e35b73b31634856c04d18c393072b9e5dcde6a32781d8b8f908c539" \
    FAKE_ZIP_MEMBERS="snell-server" \
    FAKE_ZIP_DETAILS="-rwxr-xr-x 3.0 unx 1 bx 1 defN 25-Nov-19 09:57 snell-server" \
    FAKE_EXTRACT_CONTENT="new verified binary" \
    snell_download_and_install "https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-amd64.zip" \
        "9bea1c2b9e35b73b31634856c04d18c393072b9e5dcde6a32781d8b8f908c539" "$destination" \
        || snell_test_fail "${script}: verified archive did not install"
    [[ "$(cat "${destination}/snell-server")" == "new verified binary" ]] \
        || snell_test_fail "${script}: installed binary contents are incorrect"
    [[ "$(snell_file_mode "${destination}/snell-server")" == 755 ]] \
        || snell_test_fail "${script}: installed binary is not mode 0755"

    printf 'known good binary' > "${destination}/snell-server"
    if PATH="${fake_bin}:$PATH" FAKE_SHA256=wrong FAKE_ZIP_MEMBERS=snell-server \
        FAKE_ZIP_DETAILS='-rwxr-xr-x 3.0 unx 1 bx 1 defN 25-Nov-19 09:57 snell-server' \
        FAKE_EXTRACT_CONTENT='bad binary' \
        snell_download_and_install "https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-amd64.zip" \
            "9bea1c2b9e35b73b31634856c04d18c393072b9e5dcde6a32781d8b8f908c539" "$destination"; then
        snell_test_fail "${script}: digest mismatch was accepted"
    fi
    [[ "$(cat "${destination}/snell-server")" == "known good binary" ]] \
        || snell_test_fail "${script}: digest mismatch replaced destination"

    if PATH="${fake_bin}:$PATH" FAKE_SHA256=good FAKE_ZIP_MEMBERS='../escape' \
        FAKE_ZIP_DETAILS='-rwxr-xr-x 3.0 unx 1 bx 1 defN 25-Nov-19 09:57 ../escape' \
        FAKE_EXTRACT_CONTENT=ignored snell_verify_zip archive good; then
        snell_test_fail "${script}: path traversal ZIP member was accepted"
    fi
    if PATH="${fake_bin}:$PATH" FAKE_SHA256=good FAKE_ZIP_MEMBERS=$'snell-server\nREADME' \
        FAKE_ZIP_DETAILS='-rwxr-xr-x 3.0 unx 1 bx 1 defN 25-Nov-19 09:57 snell-server' \
        FAKE_EXTRACT_CONTENT=ignored snell_verify_zip archive good; then
        snell_test_fail "${script}: ZIP with extra member was accepted"
    fi
    if PATH="${fake_bin}:$PATH" FAKE_SHA256=good FAKE_ZIP_MEMBERS=snell-server \
        FAKE_ZIP_DETAILS='lrwxrwxrwx 3.0 unx 1 bx 1 defN 25-Nov-19 09:57 snell-server' \
        FAKE_EXTRACT_CONTENT=ignored snell_verify_zip archive good; then
        snell_test_fail "${script}: symlink ZIP member was accepted"
    fi

    printf 'known good binary' > "${destination}/snell-server"
    if PATH="${fake_bin}:$PATH" \
        FAKE_SHA256="9bea1c2b9e35b73b31634856c04d18c393072b9e5dcde6a32781d8b8f908c539" \
        FAKE_ZIP_MEMBERS=snell-server \
        FAKE_ZIP_DETAILS='-rwxr-xr-x 3.0 unx 1 bx 1 defN 25-Nov-19 09:57 snell-server' \
        FAKE_EXTRACT_CONTENT='bad binary' FAKE_INSTALL_FAIL=1 \
        snell_download_and_install "https://dl.nssurge.com/snell/snell-server-v5.0.1-linux-amd64.zip" \
            "9bea1c2b9e35b73b31634856c04d18c393072b9e5dcde6a32781d8b8f908c539" "$destination"; then
        snell_test_fail "${script}: failed staged install was accepted"
    fi
    [[ "$(cat "${destination}/snell-server")" == "known good binary" ]] \
        || snell_test_fail "${script}: failed staged install replaced destination"

    echo "SNELL_HELPER_PASS: ${script}"
}

run_release_source_helper_tests() {
    local script="$1"
    local test_root helper_file repo tag invalid
    test_root="$(mktemp -d)"
    helper_file="${test_root}/release-source-helpers.sh"
    trap 'rm -rf "${test_root}"' RETURN

    sed -n '/^# XUI_RELEASE_SOURCE_HELPERS_BEGIN$/,/^# XUI_RELEASE_SOURCE_HELPERS_END$/p' "$script" > "$helper_file"
    [[ -s "$helper_file" ]] || snell_test_fail "${script}: missing release source helper block"
    # shellcheck disable=SC1090
    source "$helper_file"

    unset XUI_REPO
    [[ "$(xui_resolve_repo)" == "yushiro/3x-ui" ]] \
        || snell_test_fail "${script}: default repository is incorrect"

    XUI_REPO="example-owner/example-repo"
    [[ "$(xui_resolve_repo)" == "example-owner/example-repo" ]] \
        || snell_test_fail "${script}: valid XUI_REPO was not accepted"

    for invalid in 'owner' '/repo' 'owner/' 'owner/repo/extra' 'https://github.com/a/b' 'a/$b' 'a/../b'; do
        XUI_REPO="$invalid"
        if xui_resolve_repo > /dev/null 2>&1; then
            snell_test_fail "${script}: accepted invalid XUI_REPO: ${invalid}"
        fi
    done

    xui_validate_stable_tag v3.6.1 \
        || snell_test_fail "${script}: rejected stable tag"
    if xui_validate_stable_tag dev-latest || xui_validate_stable_tag v3.6.1-snell; then
        snell_test_fail "${script}: accepted invalid stable tag"
    fi
    xui_validate_release_ref dev-latest \
        || snell_test_fail "${script}: rejected dev-latest"

    repo="yushiro/3x-ui"
    tag="v3.6.1"
    [[ "$(xui_release_api_url "$repo")" == "https://api.github.com/repos/yushiro/3x-ui/releases/latest" ]] \
        || snell_test_fail "${script}: incorrect release API URL"
    [[ "$(xui_release_asset_url "$repo" "$tag" x-ui-linux-amd64.tar.gz)" == "https://github.com/yushiro/3x-ui/releases/download/v3.6.1/x-ui-linux-amd64.tar.gz" ]] \
        || snell_test_fail "${script}: incorrect release asset URL"
    [[ "$(xui_raw_url "$repo" "$tag" x-ui.sh)" == "https://raw.githubusercontent.com/yushiro/3x-ui/v3.6.1/x-ui.sh" ]] \
        || snell_test_fail "${script}: incorrect raw URL"
    if xui_raw_url "$repo" "$tag" unsupported-file > /dev/null 2>&1; then
        snell_test_fail "${script}: accepted unsupported raw resource"
    fi

    echo "RELEASE_SOURCE_HELPER_PASS: ${script}"
}

run_invalid_repo_no_download_test() {
    local script="$1"
    local test_root fake_bin curl_count
    test_root="$(mktemp -d)"
    fake_bin="${test_root}/bin"
    curl_count="${test_root}/curl-count"
    mkdir -p "${fake_bin}" "${test_root}/main" "${test_root}/service"
    trap 'rm -rf "${test_root}"' RETURN

    cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
printf '1\n' >> "${FAKE_CURL_COUNT:?}"
exit 99
EOF
    cat > "${fake_bin}/apt-get" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "${fake_bin}/curl" "${fake_bin}/apt-get"

    if PATH="${fake_bin}:$PATH" \
        FAKE_CURL_COUNT="${curl_count}" \
        XUI_REPO='owner/repo/extra' \
        XUI_MAIN_FOLDER="${test_root}/main/x-ui" \
        XUI_SERVICE="${test_root}/service" \
        bash "$script" > "${test_root}/output" 2>&1; then
        snell_test_fail "${script}: invalid XUI_REPO was accepted by production path"
    fi
    [[ ! -e "${curl_count}" ]] \
        || snell_test_fail "${script}: invalid XUI_REPO invoked curl"
}

assert_release_source_call_sites() {
    local script="$1"
    local raw_call_count

    rg -Fq 'xui_release_api_url "$xui_repo"' "$script" \
        || snell_test_fail "${script}: latest API call bypasses release helper"
    rg -Fq 'xui_release_asset_url "$xui_repo" "$tag_version"' "$script" \
        || snell_test_fail "${script}: release asset call bypasses release helper"
    raw_call_count="$(rg -F -c 'xui_raw_url "$xui_repo" "$tag_version"' "$script")"
    [[ "$raw_call_count" -eq 5 ]] \
        || snell_test_fail "${script}: raw resources do not consistently use tag_version"

    for upstream_url in \
        'MHSanaei/3x-ui/releases' \
        'api.github.com/repos/MHSanaei/3x-ui' \
        'raw.githubusercontent.com/MHSanaei/3x-ui'; do
        if rg -Fq "$upstream_url" "$script"; then
            snell_test_fail "${script}: upstream URL remains: ${upstream_url}"
        fi
    done
}

verify_snell_readme

if [[ "${XUI_SMOKE_VERSION}" == "--snell-helper-tests" ]]; then
    run_snell_helper_tests "${REPO_ROOT}/install.sh"
    run_snell_helper_tests "${REPO_ROOT}/update.sh"
    exit 0
fi

if [[ "${XUI_SMOKE_VERSION}" == "--release-source-tests" ]]; then
    run_release_source_helper_tests "${REPO_ROOT}/install.sh"
    run_release_source_helper_tests "${REPO_ROOT}/update.sh"
    run_invalid_repo_no_download_test "${REPO_ROOT}/install.sh"
    run_invalid_repo_no_download_test "${REPO_ROOT}/update.sh"
    assert_release_source_call_sites "${REPO_ROOT}/install.sh"
    assert_release_source_call_sites "${REPO_ROOT}/update.sh"
    exit 0
fi

if ! command -v docker > /dev/null 2>&1; then
    echo "ERROR: docker is required for this smoke test." >&2
    exit 1
fi

echo "== non-interactive install smoke test (image: $IMAGE, version: ${XUI_SMOKE_VERSION:-latest}) =="

docker run --rm \
    -v "${REPO_ROOT}/install.sh:/root/install.sh:ro" \
    -e XUI_NONINTERACTIVE=1 \
    -e XUI_SSL_MODE=none \
    -e XUI_SMOKE_VERSION="$XUI_SMOKE_VERSION" \
    -e DEBIAN_FRONTEND=noninteractive \
    "$IMAGE" bash -euo pipefail -c '
        apt-get update -qq
        apt-get install -y -qq curl tar openssl ca-certificates > /dev/null

        echo "--- running install.sh piped (no TTY), version: ${XUI_SMOKE_VERSION:-latest} ---"
        # Piping guarantees stdin is not a TTY, exercising the auto non-interactive path.
        if [ -n "${XUI_SMOKE_VERSION:-}" ]; then
            cat /root/install.sh | bash -s -- "$XUI_SMOKE_VERSION"
        else
            cat /root/install.sh | bash
        fi

        echo "--- assertions ---"
        if [ -n "${XUI_SMOKE_VERSION:-}" ]; then
            installed=$(/usr/local/x-ui/x-ui -v)
            [ "$installed" = "${XUI_SMOKE_VERSION#v}" ] \
                || { echo "FAIL: installed version $installed, want ${XUI_SMOKE_VERSION#v}"; exit 1; }
        fi

        RESULT=/etc/x-ui/install-result.env
        test -f "$RESULT" || { echo "FAIL: $RESULT missing"; exit 1; }

        perms=$(stat -c %a "$RESULT")
        [ "$perms" = "600" ] || { echo "FAIL: $RESULT perms=$perms (want 600)"; exit 1; }

        # shellcheck disable=SC1090
        . "$RESULT"
        [ -n "${XUI_USERNAME:-}" ] && [ "$XUI_USERNAME" != "admin" ] \
            || { echo "FAIL: username missing or still admin"; exit 1; }
        [ -n "${XUI_PASSWORD:-}" ] && [ "$XUI_PASSWORD" != "admin" ] \
            || { echo "FAIL: password missing or still admin"; exit 1; }
        [ -n "${XUI_PANEL_PORT:-}" ] || { echo "FAIL: port missing"; exit 1; }

        # No default admin in the DB.
        /usr/local/x-ui/x-ui setting -show | grep -q "hasDefaultCredential: false" \
            || { echo "FAIL: hasDefaultCredential is not false"; exit 1; }

        echo "--- verifying the panel serves HTTP ---"
        cd /usr/local/x-ui
        ./x-ui > /tmp/xui.log 2>&1 &
        xpid=$!
        for _ in $(seq 1 15); do
            code=$(curl -s -o /dev/null -w "%{http_code}" \
                "http://127.0.0.1:${XUI_PANEL_PORT}/${XUI_WEB_BASE_PATH}/" 2>/dev/null || true)
            case "$code" in 200|301|302|307|308) break ;; esac
            sleep 1
        done
        kill "$xpid" 2>/dev/null || true
        echo "panel HTTP status: ${code:-none}"
        case "${code:-}" in
            200|301|302|307|308) : ;;
            *) echo "FAIL: panel did not serve (status ${code:-none})"; tail -n 30 /tmp/xui.log; exit 1 ;;
        esac

        echo "SMOKE_PASS: user=$XUI_USERNAME port=$XUI_PANEL_PORT path=$XUI_WEB_BASE_PATH"
    '

echo "== non-interactive smoke test PASSED =="
