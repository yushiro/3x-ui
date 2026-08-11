# Fork 发布源一致性 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 fork 的安装、升级、自更新和稳定 Release 全程使用 `yushiro/3x-ui`（或经过校验的 `XUI_REPO` 覆盖值），不再混装原项目二进制或可变分支脚本。

**Architecture:** 三个可独立下载执行的 Bash 入口保留各自的同名、小型发布源 helper block，以相同规则校验仓库和 release ref、构造 GitHub URL。稳定路径先解析并校验 `vX.Y.Z` tag，再让归档、脚本和 service 文件绑定该 tag；开发路径只使用 `dev-latest`。现有 Release workflow 负责将稳定 tag 发布为非 prerelease，Shell smoke 以 fake downloader 和静态契约完成无网络回归验证。

**Tech Stack:** Bash、GitHub Actions YAML、现有 `deploy/test/smoke-noninteractive.sh` 测试框架、Markdown。

## Global Constraints

- 默认发布源必须是精确字面量 `yushiro/3x-ui`；不得静默退回 `MHSanaei/3x-ui`。
- `XUI_REPO` 只接受安全的 `owner/repository` 形式，并在任何网络、解压或删除动作前验证。
- 稳定 tag 只接受 `^v[0-9]+\.[0-9]+\.[0-9]+$`；开发通道只接受字面量 `dev-latest`。
- 选定 tag 后，3x-ui 归档、`install.sh`、`update.sh`、`x-ui.sh`、`x-ui.rc` 和 `x-ui.service*` 必须使用同一 `${XUI_REPO}/${TAG}`。
- `dev-latest` 必须保持 prerelease 且 `latest=false`；稳定 `v*.*.*` Release 必须是 `prerelease: false`。
- Xray、Snell/NSSurge、MTProto 和规则数据等第三方来源不得改变。
- 所有测试必须离线执行，不写 `/usr/bin`、`/etc` 或真实安装目录。
- 本计划不创建 tag、不发布 Release、不部署服务器。

---

### Task 1: 安装器与更新器使用同一 fork/tag

**Files:**
- Modify: `deploy/test/smoke-noninteractive.sh`
- Modify: `install.sh`
- Modify: `update.sh`

**Interfaces:**
- Consumes: 环境变量 `XUI_REPO`、安装器位置参数 tag、更新器环境变量 `XUI_UPDATE_TAG`、现有 `arch()` 与下载器。
- Produces: 两个脚本中语义一致并位于 `# XUI_RELEASE_SOURCE_HELPERS_BEGIN` / `# XUI_RELEASE_SOURCE_HELPERS_END` 标记之间的函数：
  - `xui_resolve_repo`：输出已校验仓库，默认 `yushiro/3x-ui`，失败返回非零。
  - `xui_validate_stable_tag TAG`：只接受 `vX.Y.Z`。
  - `xui_validate_release_ref REF`：接受稳定 tag 或 `dev-latest`。
  - `xui_release_api_url REPO`：输出该 fork 的 `releases/latest` API URL。
  - `xui_release_asset_url REPO TAG ASSET`：输出同仓库、同 tag 的 Release asset URL。
  - `xui_raw_url REPO REF FILE`：仅为固定项目文件构造 raw URL。

- [ ] **Step 1: 为发布源 helper 写失败测试**

在 `deploy/test/smoke-noninteractive.sh` 增加 `run_release_source_helper_tests SCRIPT`，从标记块提取 helper 到临时文件并断言：

```bash
unset XUI_REPO
[[ "$(xui_resolve_repo)" == "yushiro/3x-ui" ]]

XUI_REPO="example-owner/example-repo"
[[ "$(xui_resolve_repo)" == "example-owner/example-repo" ]]

for invalid in 'owner' '/repo' 'owner/' 'owner/repo/extra' 'https://github.com/a/b' 'a/$b' 'a/../b'; do
    XUI_REPO="$invalid"
    if xui_resolve_repo >/dev/null 2>&1; then
        snell_test_fail "$script accepted invalid XUI_REPO: $invalid"
    fi
done

xui_validate_stable_tag v3.6.1
! xui_validate_stable_tag dev-latest
! xui_validate_stable_tag v3.6.1-snell
xui_validate_release_ref dev-latest

repo="yushiro/3x-ui"
tag="v3.6.1"
[[ "$(xui_release_api_url "$repo")" == "https://api.github.com/repos/yushiro/3x-ui/releases/latest" ]]
[[ "$(xui_release_asset_url "$repo" "$tag" x-ui-linux-amd64.tar.gz)" == "https://github.com/yushiro/3x-ui/releases/download/v3.6.1/x-ui-linux-amd64.tar.gz" ]]
[[ "$(xui_raw_url "$repo" "$tag" x-ui.sh)" == "https://raw.githubusercontent.com/yushiro/3x-ui/v3.6.1/x-ui.sh" ]]
```

为脚本增加 `--release-source-tests` 模式，依次测试 `install.sh` 与 `update.sh`，不进入现有 Docker smoke。

- [ ] **Step 2: 运行测试并确认 RED**

Run:

```bash
bash deploy/test/smoke-noninteractive.sh --release-source-tests
```

Expected: FAIL，因为两个脚本尚无 `XUI_RELEASE_SOURCE_HELPERS` 标记块或 `xui_resolve_repo`。

- [ ] **Step 3: 在两个脚本实现最小 helper block**

在 `install.sh` 与 `update.sh` 的公共函数区域加入等价实现；仓库正则必须支持单字符段且拒绝额外路径：

```bash
# XUI_RELEASE_SOURCE_HELPERS_BEGIN
XUI_DEFAULT_REPO="yushiro/3x-ui"

xui_resolve_repo() {
    local repo="${XUI_REPO:-${XUI_DEFAULT_REPO}}"
    [[ "$repo" =~ ^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?/[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$ ]] || {
        echo "Invalid XUI_REPO: ${repo}" >&2
        return 2
    }
    printf '%s\n' "$repo"
}

xui_validate_stable_tag() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; }
xui_validate_release_ref() { [[ "$1" == "dev-latest" ]] || xui_validate_stable_tag "$1"; }
xui_release_api_url() { printf 'https://api.github.com/repos/%s/releases/latest\n' "$1"; }
xui_release_asset_url() { printf 'https://github.com/%s/releases/download/%s/%s\n' "$1" "$2" "$3"; }
xui_raw_url() {
    case "$3" in
        install.sh|update.sh|x-ui.sh|x-ui.rc|x-ui.service.debian|x-ui.service.arch|x-ui.service.rhel) ;;
        *) return 2 ;;
    esac
    xui_validate_release_ref "$2" || return 2
    printf 'https://raw.githubusercontent.com/%s/%s/%s\n' "$1" "$2" "$3"
}
# XUI_RELEASE_SOURCE_HELPERS_END
```

调用处必须引用命令替换结果；不得使用 `eval`，不得把未经 helper 校验的环境值直接拼入 URL。

- [ ] **Step 4: 让 `install.sh` 先确定唯一 repo/tag，再执行下载**

在任何下载或删除旧安装前：

```bash
xui_repo="$(xui_resolve_repo)" || exit 1
export XUI_REPO="$xui_repo"
```

无参数安装通过 `xui_release_api_url "$xui_repo"` 查询最新 tag，查询结果必须经 `xui_validate_stable_tag`；显式 `dev` 归一成 `dev-latest`，其他显式 tag 也必须验证。归档 URL 使用：

```bash
archive_url="$(xui_release_asset_url "$xui_repo" "$tag_version" "x-ui-linux-$(arch).tar.gz")" || exit 1
```

`/usr/bin/x-ui` 临时脚本、Alpine `x-ui.rc`、service fallback 均以 `xui_raw_url "$xui_repo" "$tag_version" FIXED_FILE` 下载。删除所有属于 3x-ui 的 `MHSanaei/3x-ui` URL，下载缺失或空文件时维持现有非零退出语义。

- [ ] **Step 5: 让 `update.sh` 使用相同 repo/tag 边界**

`XUI_UPDATE_TAG` 非空时验证并使用；为空时从 fork latest API 解析稳定 tag。归档、`x-ui.sh`、`x-ui.rc` 和 service fallback 都使用同一个 `tag_version`。在停止服务或删除旧文件之前完成 repo/tag 校验及必要下载，禁止失败后切换 upstream。

- [ ] **Step 6: 扩展测试验证真实调用点的 tag 一致性**

在 focused smoke 中增加静态/受控执行断言：两个脚本的 3x-ui API、Release 和 raw URL 均经 helper 构造；所有 raw resource 调用都传 `tag_version`；生产路径不包含以下字面量：

```bash
MHSanaei/3x-ui/releases
api.github.com/repos/MHSanaei/3x-ui
raw.githubusercontent.com/MHSanaei/3x-ui
```

测试还必须断言无效 `XUI_REPO` 在 fake curl 调用计数为零时退出。

- [ ] **Step 7: 运行 GREEN 验证并提交**

Run:

```bash
bash -n install.sh update.sh deploy/test/smoke-noninteractive.sh
bash deploy/test/smoke-noninteractive.sh --release-source-tests
bash deploy/test/smoke-noninteractive.sh --snell-helper-tests
```

Expected: 全部退出 0，输出每个脚本的 release-source PASS 和既有 `SNELL_HELPER_PASS`。

Commit:

```bash
git add install.sh update.sh deploy/test/smoke-noninteractive.sh
git commit -m "fix: use fork release source in installers"
```

### Task 2: 管理脚本跨操作传播 fork 和 release ref

**Files:**
- Modify: `deploy/test/smoke-noninteractive.sh`
- Modify: `x-ui.sh`

**Interfaces:**
- Consumes: Task 1 的相同 helper 契约、`XUI_REPO`、菜单函数 `install`、`update`、`update_dev`、`update_shell` 与 `replace_xui_script`。
- Produces: 所有下载 3x-ui 脚本的菜单路径均导出已校验 `XUI_REPO`，稳定路径传纯 `vX.Y.Z`，Dev 路径传 `dev-latest`。

- [ ] **Step 1: 为 `x-ui.sh` 写失败测试**

让 `--release-source-tests` 同时提取并测试 `x-ui.sh` helper，并增加静态契约：

```bash
for required in install update update_dev update_shell; do
    # 对应函数体必须使用 xui_raw_url 或调用解析出的 URL，且不得出现 upstream URL。
    extract_function "$REPO_ROOT/x-ui.sh" "$required" > "$test_root/$required.sh"
    grep -q 'xui_' "$test_root/$required.sh"
done

! grep -Eq 'raw\.githubusercontent\.com/MHSanaei/3x-ui|github\.com/MHSanaei/3x-ui/raw' "$REPO_ROOT/x-ui.sh"
```

fake curl 记录 URL 与子 shell 环境，分别模拟 latest tag `v3.6.1` 和 Dev，断言稳定操作下载 `${XUI_REPO}/v3.6.1/...` 并向子脚本传 `XUI_UPDATE_TAG=v3.6.1`，Dev 操作只使用 `${XUI_REPO}/dev-latest/...`。

- [ ] **Step 2: 运行测试并确认 RED**

Run:

```bash
bash deploy/test/smoke-noninteractive.sh --release-source-tests
```

Expected: FAIL，因为 `x-ui.sh` 仍含 `MHSanaei/3x-ui` raw URL，且没有 release source helper/传播。

- [ ] **Step 3: 在 `x-ui.sh` 加入相同 helper 与稳定 tag 解析**

加入与 Task 1 同名、同正则、同固定文件白名单的 helper block。增加：

```bash
xui_latest_stable_tag() {
    local repo api tag
    repo="$(xui_resolve_repo)" || return 1
    api="$(xui_release_api_url "$repo")" || return 1
    tag="$(curl -fsSL --retry 5 --retry-delay 3 --connect-timeout 15 "$api" |
        sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
        head -n 1)" || return 1
    xui_validate_stable_tag "$tag" || return 1
    printf '%s\n' "$tag"
}
```

脚本启动时先解析并 `export XUI_REPO`；无效覆盖立即退出，不进入菜单或网络调用。

- [ ] **Step 4: 改造稳定、Dev 和自更新菜单路径**

- `install`：无版本时先解析稳定 tag；旧版本参数必须通过稳定 tag 校验；从该 tag 下载 `install.sh` 并把同一 tag 传给它。
- `update`：先解析稳定 tag，从该 tag 下载 `update.sh`，以 `XUI_UPDATE_TAG="$tag" XUI_REPO="$XUI_REPO"` 调用。
- `update_dev`：ref 和 `XUI_UPDATE_TAG` 均为 `dev-latest`，不查询 stable latest。
- `update_shell`、启动时稳定自更新：先解析稳定 tag，再从该 tag 下载 `x-ui.sh`。
- 所有 `replace_xui_script` 调用只接收 `xui_raw_url` 的结果；删除 upstream 与 `main`/`master` raw URL。

- [ ] **Step 5: 运行 GREEN 验证并提交**

Run:

```bash
bash -n x-ui.sh deploy/test/smoke-noninteractive.sh
bash deploy/test/smoke-noninteractive.sh --release-source-tests
```

Expected: 退出 0；默认 repo、覆盖传播、stable/dev ref 与 upstream-absence 断言全部通过。

Commit:

```bash
git add x-ui.sh deploy/test/smoke-noninteractive.sh
git commit -m "fix: keep panel updates on fork releases"
```

### Task 3: 稳定 Release 语义、入口文档与最终验证

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `deploy/test/smoke-noninteractive.sh`
- Modify: `deploy/cloud-init/cloud-init.yaml`
- Modify: `README.md`

**Interfaces:**
- Consumes: GitHub tag push `v*.*.*`、现有 Linux/Windows artifact、Task 1 默认安装入口。
- Produces: stable tag Release 为非 prerelease；`dev-latest` 仍为 prerelease 且不占用 latest；所有默认生产入口指向 fork。

- [ ] **Step 1: 写 workflow 与入口失败测试**

在 `--release-source-tests` 中增加：

```bash
stable_uploads="$(grep -A12 'name: Upload files to GH release' "$REPO_ROOT/.github/workflows/release.yml")"
[[ "$(printf '%s\n' "$stable_uploads" | grep -c 'prerelease: false')" -eq 2 ]]
grep -q 'gh release edit dev-latest --prerelease --latest=false' "$REPO_ROOT/.github/workflows/release.yml"
grep -q 'gh release create dev-latest --prerelease --latest=false' "$REPO_ROOT/.github/workflows/release.yml"
grep -q 'raw.githubusercontent.com/yushiro/3x-ui/main/install.sh' "$REPO_ROOT/README.md"
grep -q 'raw.githubusercontent.com/yushiro/3x-ui/main/install.sh' "$REPO_ROOT/deploy/cloud-init/cloud-init.yaml"
```

同时断言 README 说明：无参数安装依赖 fork 已存在包含当前架构 asset 的稳定 Release，并展示 `XUI_REPO=owner/repo` 覆盖语法。

- [ ] **Step 2: 运行测试并确认 RED**

Run:

```bash
bash deploy/test/smoke-noninteractive.sh --release-source-tests
```

Expected: FAIL，因为两个稳定上传步骤仍为 `prerelease: true`，README/cloud-init 仍指 upstream。

- [ ] **Step 3: 修正 Release workflow**

仅把两个 tag-only `svenstaro/upload-release-action` 步骤改为：

```yaml
prerelease: false
```

保持 tag trigger `v*.*.*`、`contents: write`、artifact 名称、`dev-latest --prerelease --latest=false` 和第三方下载不变。不得让 `workflow_dispatch` 自动发布稳定 Release；稳定发布仍只由 tag push 触发。

- [ ] **Step 4: 修正默认入口与 README**

把 README 主安装命令和 cloud-init bootstrap 改为 `https://raw.githubusercontent.com/yushiro/3x-ui/main/install.sh`。README 增加简短说明：

```bash
# 默认安装 yushiro/3x-ui 最新稳定 Release
bash <(curl -Ls https://raw.githubusercontent.com/yushiro/3x-ui/main/install.sh)

# 显式覆盖为另一个兼容 fork
XUI_REPO=owner/repository bash <(curl -Ls https://raw.githubusercontent.com/owner/repository/main/install.sh)
```

说明首次无参数安装只有在 fork 已发布稳定 `vX.Y.Z` 且包含当前架构 `x-ui-linux-<arch>.tar.gz` 时才成功；不要宣称已经发布或可用于生产。

- [ ] **Step 5: 运行聚焦和回归验证**

Run:

```bash
bash -n install.sh update.sh x-ui.sh deploy/test/smoke-noninteractive.sh
bash deploy/test/smoke-noninteractive.sh --release-source-tests
bash deploy/test/smoke-noninteractive.sh --snell-helper-tests
git diff --check
```

Expected: 全部退出 0；两个 stable upload 均为 false；两个 dev 命令保持 `--prerelease --latest=false`；三个入口脚本没有原项目 3x-ui 生产 URL。

- [ ] **Step 6: 复核范围并提交**

Run:

```bash
git diff --name-only HEAD~2..HEAD
git diff -- .github/workflows/release.yml README.md deploy/cloud-init/cloud-init.yaml deploy/test/smoke-noninteractive.sh
```

确认没有 tag、二进制、Release asset、第三方 URL 或部署状态变更。

Commit:

```bash
git add .github/workflows/release.yml README.md deploy/cloud-init/cloud-init.yaml deploy/test/smoke-noninteractive.sh
git commit -m "ci: publish stable fork releases"
```

### Task 4: 完整完成门

**Files:**
- Verify only: all files changed by Tasks 1–3

**Interfaces:**
- Consumes: 三个实现提交。
- Produces: 可供独立 Terra reviewer 检查的单一完整 diff；不产生 Release 或部署副作用。

- [ ] **Step 1: 运行最终新鲜验证**

Run:

```bash
bash -n install.sh update.sh x-ui.sh deploy/test/smoke-noninteractive.sh
bash deploy/test/smoke-noninteractive.sh --release-source-tests
bash deploy/test/smoke-noninteractive.sh --snell-helper-tests
git diff --check e8134773..HEAD
git status -sb
```

Expected: 所有命令退出 0；工作树干净；分支只领先于已提交变更。

- [ ] **Step 2: 逐条验收安全边界**

Run:

```bash
rg -n 'MHSanaei/3x-ui' install.sh update.sh x-ui.sh deploy/cloud-init/cloud-init.yaml
rg -n 'prerelease:|latest=false' .github/workflows/release.yml
rg -n 'dl\.nssurge\.com|XTLS/Xray-core|mhsanaei/mtg-multi|Loyalsoldier' install.sh update.sh x-ui.sh .github/workflows/release.yml
```

Expected: 第一条无匹配；第二条显示两个 stable `false` 和 Dev `--latest=false`；第三条证明第三方来源仍存在且未被替换。

- [ ] **Step 3: 交给独立 reviewer**

Reviewer 只检查：验收标准缺失、URL/tag 混装、覆盖值注入、失败时 upstream 回退、稳定 Release 仍被标记 prerelease，以及 fix 引入的直接回归。最多两轮 HIGH_RISK 修复；不创建 tag、Release 或部署。
