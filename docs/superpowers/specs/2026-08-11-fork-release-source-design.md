# Fork 发布源一致性设计

## 目标与范围

本 fork 的安装、稳定更新、开发更新和菜单自更新必须始终从同一个
3x-ui 源获取发布物与脚本，避免当前 `install.sh` 从
`MHSanaei/3x-ui` 下载二进制/脚本而本地源码包含 Snell 的混合安装。

默认发布源为 `yushiro/3x-ui`。操作者可以显式设置 `XUI_REPO=owner/repo`
覆盖默认值。实现边界仅限脚本、测试、README 与 GitHub Actions 工作流；
不创建 tag、不发布 release、不部署服务器。首次无参数真实安装依赖该 fork
已有一个包含目标架构归档的稳定发布。

Xray、Snell/NSSurge、MTProto、规则数据及其他第三方下载源保持原样。

## 源解析与安全约束

三个入口脚本 `install.sh`、`update.sh` 与 `x-ui.sh` 使用同一份可测试的源解析
规则（可在各脚本中以等价 Bash 函数实现）：

1. `XUI_REPO` 未设置或为空时取 `yushiro/3x-ui`；非空值才是显式覆盖。
2. 显式覆盖值必须恰好匹配 GitHub owner/repository 形式：两段均由字母、数字、
   `.`、`_`、`-` 组成，首尾为字母或数字，且不含空白、斜杠以外的路径分隔符、
   引号、反引号、`$`、控制字符或 URL scheme。
3. 无效值立即以非零状态失败并给出“无效 XUI_REPO”错误；不得拼接 URL、执行
   其内容，或退回 `MHSanaei/3x-ui`。

解析后的值是本次执行唯一的发布源。`install.sh`、`update.sh` 与 `x-ui.sh`
中所有属于 3x-ui 的 release API、release archive、raw 脚本及 service URL
都只能由它和下表的受控 ref/path 构造；第三方 URL 不在此规则内。

| 用途 | URL 形式 |
| --- | --- |
| 稳定 release 查询 | `https://api.github.com/repos/${XUI_REPO}/releases/latest` |
| 指定 release 归档 | `https://github.com/${XUI_REPO}/releases/download/${TAG}/x-ui-linux-${ARCH}.tar.gz` |
| 项目脚本和 service 原始文件 | `https://raw.githubusercontent.com/${XUI_REPO}/${REF}/${FILE}` |

其中 `TAG` 只能是已验证的稳定 tag 或字面量 `dev-latest`，`REF` 仅等于
该 `TAG`，`ARCH` 仅接受脚本已支持的规范架构名，`FILE` 仅为脚本中列出的固定项目
文件名。这样不会把不可信输入解释为 URL 路径或 shell 语法。

所有变量在 shell 中必须双引号引用；tag、架构和文件名不得来自未校验的可执行
文本。任何 release 查询、归档、脚本或 service 文件缺失、为空、下载失败、解压
失败或不含预期二进制时均失败关闭，不尝试 upstream 或可变分支作为后备源。

## 发布通道与同版本资源

稳定发布的 tag 只接受 `v<主版本>.<次版本>.<修订版本>`，即
`^v[0-9]+\.[0-9]+\.[0-9]+$`。无参数安装和稳定更新先解析
`releases/latest`，验证该 tag，再以它下载归档。

一旦解析出稳定 tag，`REF` 固定为该 tag：归档后的 `/usr/bin/x-ui`、
`x-ui.sh`、缺失时的 `x-ui.service*`/`x-ui.rc` 回退文件，以及由菜单触发的稳定
脚本下载都使用同一 tag。优先安装归档中随 package 提供的 `x-ui.sh` 与 service
文件；归档没有某个所需脚本或 service 文件时，只能从同一 `${TAG}` 的 raw URL
取得它。不得把稳定 release 二进制与 `main`/`master` 的可变脚本或 service 混用。

开发通道唯一使用固定 tag `dev-latest`：`install.sh dev` 与
`install.sh dev-latest` 归一为该 tag，`x-ui.sh` 的 Dev 更新向 `update.sh` 传递
`XUI_UPDATE_TAG=dev-latest` 与当前 `XUI_REPO`，并以 `dev-latest` 作为脚本与
归档 ref。它是 prerelease，不是稳定版本，不能改变 stable 查询结果。

工作流对与该稳定 tag 格式相符的 `v*.*.*` tag 发布稳定 release，并显式设置
`prerelease: false`；主分支每次构建只更新 `dev-latest` prerelease，并显式使用
`--latest=false`。因此 GitHub 的
`releases/latest` 始终指向最后一个稳定 tag。

## 入口与传播行为

`x-ui.sh` 的安装、稳定更新、Dev 更新、自更新和旧版本安装提示均构造 fork URL，
并在调用远程 `install.sh`/`update.sh` 时导出同一个已校验的 `XUI_REPO`。用户在
当前调用中显式给出的覆盖值必须传入下一层脚本；未给出时每一层独立采用 fork
默认值。稳定更新和稳定菜单自更新必须先查询并验证选定的稳定 tag，再下载
`update.sh`、`x-ui.sh` 或 service 资源；不能以 `main`/`master` 自更新后再安装
某个稳定归档。Dev 路径使用 `dev-latest`。旧版本安装只接受同一稳定 tag 格式，
并以该 tag 下载 fork 的 `install.sh` 和 archive。安装和更新入口所需的 raw
`install.sh`/`update.sh` 也遵循同一 `${XUI_REPO}/${REF}` 规则。不再有指向
upstream 的生产 URL。

README 的默认安装命令改为 fork 的原始安装脚本；示例可说明
`XUI_REPO=owner/repo` 覆盖，但不把 upstream 作为生产回退。README 同时说明：
无参数安装只有在 fork 的稳定 release 已发布且包含对应归档时才会成功。

## 测试与验收

shell 回归测试必须使用 fake `curl`/下载器和临时目录，不访问网络、不写系统路径，
并覆盖：

1. 默认值所有 release API、归档、raw script 与 service URL 都含
   `yushiro/3x-ui`。
2. 合法 `XUI_REPO` 覆盖贯穿 install、update 与 x-ui 菜单到下级脚本；非法覆盖
   在任何网络/解压/删除动作前失败。
3. 稳定 tag 解析后，归档、`x-ui.sh` 和 service 回退 URL 使用完全相同的 tag；
   `dev-latest` 使用自身 ref。
4. 生产脚本路径没有 `MHSanaei/3x-ui` 后备 URL，也不会在 fork 失败时静默改用
   upstream。
5. release workflow 将 `v*.*.*` 当稳定发布，将 `dev-latest` 保持 prerelease
   且 `latest=false`。
6. `bash -n`、现有非交互 smoke 和新增 shell 回归均通过；README 命令与默认源
   一致。

验收不包含真实 release 发布或服务器安装。若 fork 尚无稳定 release，测试只验证
失败关闭和 URL/版本选择，不把无参数真实安装标记为可用。

## 风险与失败处理

GitHub API 限流、私有仓库权限不足、release/tag 不存在、归档缺少当前架构资产、
raw 文件不存在以及下载得到空文件均是明确错误，而不是切换来源的理由。稳定资源
使用不可变 tag 降低二进制与管理脚本不兼容风险；`dev-latest` 的可移动性仅限被
明确选择的开发通道。覆盖值的格式验证和全程引用防止 URL 注入、参数拆分与 shell
注入。
