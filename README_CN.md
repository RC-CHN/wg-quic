# wg-quic

[English](README.md) | 简体中文

仓库：<https://github.com/RC-CHN/wg-quic>

`wg-quic` 使用 QUIC DATAGRAM 帧传输完整、已加密的 WireGuard 数据报。它保留
WireGuard 的接口和配置模型，但将外层 WireGuard UDP 传输替换为 QUIC，并提供
可选的自适应 FEC 和 Salamander 风格数据包混淆。

> [!IMPORTANT]
> 隧道两端都必须运行 `wg-quic`。虽然它使用熟悉的 WireGuard 密钥和
> `wg-quick` 风格配置文件，但标准 WireGuard 端点不能连接 `wg-quic` 端点。

当前公开版本为
[`v0.3.1`](https://github.com/RC-CHN/wg-quic/releases/tag/v0.3.1)。

## 平台支持情况

| 平台 | 可用软件包 | 服务或界面 | 当前验证范围 |
|---|---|---|---|
| Linux | amd64、arm64 CLI 压缩包；amd64 桌面 Deb | systemd；可选 Tauri 桌面端 | amd64 原生测试、race 测试和双节点特权 TUN 互通；arm64 交叉编译 |
| Windows | amd64、arm64 CLI 包；x64 桌面 MSI | Wintun、每隧道 SCM 服务和 Tauri 桌面端 | 已安装 x64 MSI、LocalSystem 服务、Wintun、地址/MTU/DNS/路由、升级、状态与清理；arm64 交叉编译 |
| FreeBSD | amd64、arm64 CLI 压缩包 | rc.d | FreeBSD 14 amd64 原生测试；arm64 交叉编译 |
| OPNsense | 26.1/FreeBSD 14 和 26.7/FreeBSD 15 的 amd64 私有包 | `VPN > wg-quic`、Dashboard 小组件、configd 和 `quicN` 接口 | 两个版本均通过包验证和 QEMU 运行测试，并验证 Linux 到 OPNsense 的流量 |
| OpenWrt | OpenWrt 25.12.5 `armsr/armv8`、`x86/64` APK | procd 和 UCI 多实例服务 | 两种 APK 均通过 SDK 构建和元数据检查；ARM64 还通过完整 QEMU 安装、流量、重启、hooks 和卸载测试 |
| macOS、Android、iOS | 无 | 无 | 当前不支持 |

从 `v0.3.1` 开始，Release workflow 除上传
`release-openwrt-armsr-armv8` 和 `release-openwrt-x86-64` CI artifacts 外，
还会把两个 OpenWrt APK 加入正式 Release。

## 应该使用哪个命令

项目有两个职责明确的可执行程序：

- `wg-quic` 是底层用户态数据面，负责 WireGuard、QUIC、TUN 设备和本地状态接口。
- `wg-quic-quick` 是正常使用入口，负责地址、路由、MTU、DNS、hooks、端点路由
  固定和各平台服务管理。

正常隧道应使用 `wg-quic-quick`。直接执行 `wg-quic run` 不会应用 `Address`、
路由、DNS、`PreUp`、`PostUp`、`PreDown` 或 `PostDown`。底层 core 现在会拒绝
带 hooks 的配置，避免静默忽略主机策略。

常用命令：

```sh
wg-quic-quick check wg0
sudo wg-quic-quick run wg0       # 前台运行完整隧道生命周期
sudo wg-quic-quick up wg0        # 通过平台服务管理器启动
wg-quic-quick show wg0
wg-quic-quick show wg0 --json
sudo wg-quic-quick down wg0
```

### 运行时 peer 与 DDNS 管理（当前开发树）

当前 `main` 已支持在线 peer reconciliation 和自动 DDNS。已经发布的 v0.3.1
制品早于这套实现，并不包含这些能力；请使用当前开发版或后续版本。

首先检查运行中的 supervisor。Unix 上的 quick 管理 socket 权限为 `0600`，读取
完整状态需要 root：

```sh
sudo wg-quic-quick show wg0 --json
```

`capabilities` 数组包含 `peer_reconcile_v1` 时才能在线修改 peer，包含
`endpoint_refresh_v1` 时才能手动触发 DDNS 刷新。状态还会显示
`supervisor_epoch`、`desired_generation`、`persistent_drift`、恢复状态，以及每个
peer 的配置域名、当前数字 endpoint、DNS 候选、下次刷新时间、解析错误、
endpoint generation、已认证 endpoint、会话和流量计数。

CLI 是特权本地管理 API 的标准客户端：

| CLI | 协议 operation | 所需 capability | 作用 |
| --- | --- | --- | --- |
| `show --json` | `status` | `management_protocol_v1` | 查看运行状态、持久化、peer、DDNS、恢复和事务 |
| `reload` | `reload` | `peer_reconcile_v1` | 重新读取完整正式配置并同步运行状态 |
| `reconcile` | `reconcile` | `peer_reconcile_v1` | 使用 epoch/generation CAS 校验并应用受保护候选配置 |
| `transaction-status` | `transaction_status` | 管理协议 | 按 request ID 恢复已接受事务的结果 |
| `refresh-endpoints` | `refresh_endpoints` | `endpoint_refresh_v1` | 立即刷新全部域名 peer 或指定公钥 |

它不是远程 HTTP API。Linux/OpenWrt 使用
`/run/wg-quic/<接口>.manage.sock`，FreeBSD/OPNsense 使用
`/var/run/wg-quic/<接口>.manage.sock`，Windows 使用 ACL 保护的
`\\.\pipe\wg-quic-quick-<接口>` named pipe。每个连接只承载一条有大小限制的
protocol-v1 JSON 请求和一条响应。控制器通常应调用 CLI 或 typed Go client，不能
把这些本地端点暴露到网络。

#### 增加、修改或删除 peer

每次输入都是**完整 desired 配置**，不是单个 peer patch。增加 `[Peer]` 段即添加，
修改该段即更新，不再包含某个段即删除对应 peer。普通管理员应先原子替换正式
配置，再执行 `reload`。以下 Linux/OpenWrt 示例把临时文件写入同一个 root-only
目录，因此最后的 rename 是原子的：

```sh
sudo install -o root -g root -m 0600 ./wg0.next.conf \
  /etc/wg-quic/.wg0.conf.next
sudo mv /etc/wg-quic/.wg0.conf.next /etc/wg-quic/wg0.conf
sudo wg-quic-quick reload wg0 --json
```

FreeBSD/OPNsense 应改用 `/usr/local/etc/wg-quic/`。Windows manager 会在
`%ProgramData%\wg-quic\interfaces` 下完成受保护的候选文件和正式文件操作。
`reload` 会自行读取状态、填写当前 CAS tuple 并生成 request ID。如果配置有效但
运行时提交失败，`show --json` 会报告 `persistent_drift=true`，不会假装正式文件
已经回滚。

需要“先提交运行时、再提升正式文件”的自动化控制器应使用 candidate workflow。
候选文件必须放在 root-only 路径；超时重试必须沿用同一个 request ID；只有结果为
`committed` 或 `no_op` 后才能提升完全相同的字节：

```sh
sudo wg-quic-quick show wg0 --json
# 从响应复制 supervisor_epoch 和 desired_generation。

sudo install -d -o root -g root -m 0700 /etc/wg-quic/.candidates
sudo install -o root -g root -m 0600 ./wg0.next.conf \
  /etc/wg-quic/.candidates/wg0.peer-change-01.conf

sudo wg-quic-quick reconcile wg0 \
  /etc/wg-quic/.candidates/wg0.peer-change-01.conf \
  --expected-epoch EPOCH --expected-generation N \
  --request-id peer-change-01 --json

# 响应丢失时沿用同一个 ID；不得用这个 ID 提交已经变化的内容。
sudo wg-quic-quick transaction-status wg0 \
  --request-id peer-change-01 --json

# 成功后，在同一文件系统内原子提升完全相同的候选文件。
sudo mv /etc/wg-quic/.candidates/wg0.peer-change-01.conf \
  /etc/wg-quic/wg0.conf
```

可热更新字段包括 peer 增删、普通 `AllowedIPs`、`Endpoint`、
`PersistentKeepalive` 和 `peer.fec-latency`。新增 peer 可以带 `PresharedKey`；修改
运行中 peer 的 PresharedKey 需要重启。接口密钥/地址/listen port/fwmark/MTU/
DNS/table/hooks/全局传输策略、自动全隧道切换和运行中 PresharedKey 轮换会在任何
mutation 前返回 `restart_required`。虽然现有 peer 省略 PresharedKey 时本次运行时
事务可以继承活动密钥，但需要持久化的完整配置仍应保留全部密钥。
`SaveConfig = true` 继续明确拒绝。

#### DDNS 行为与手动刷新

只要 `Endpoint` 使用域名，例如 `Endpoint = edge.example.com:443`，该 peer 就会
自动进入 DDNS 管理。supervisor 会解析所有可用 A/AAAA 候选并定期刷新。当前系统
resolver 不提供 TTL，因此使用一分钟保守基础间隔并加入 jitter（策略上下限为
30 秒和 30 分钟）；主机路由变化或连续传输恢复失败也能提前触发刷新或候选轮换。

```sh
# 立即刷新所有使用域名的 peer。
sudo wg-quic-quick refresh-endpoints wg0 --json

# 使用完整 base64 公钥刷新一个 peer。
sudo wg-quic-quick refresh-endpoints wg0 \
  --peer 'PEER_PUBLIC_KEY_BASE64' --json

# 查看 selected_endpoint、dns_candidates、next_refresh_at 和解析错误。
sudo wg-quic-quick show wg0 --json
```

DNS 答案仅改变顺序时不会移动健康 peer。timeout、SERVFAIL 或 NXDOMAIN 会保留上次
可用 endpoint，并在状态中暴露错误。新地址只有在外层路由准备完成且 WireGuard
流量认证新的 endpoint generation 后才会发布；失败会回滚候选地址和路由 lease。
DDNS 只推进 `endpoint_generation`，不改变 `desired_generation`，也不会重写配置。

完整 JSON、事务、安全、回滚和控制器契约见
[`docs/RUNTIME-PEER-RECONCILIATION.md`](docs/RUNTIME-PEER-RECONCILIATION.md)。

这里的“全平台”指同一协议和事务语义，不代表每种 CPU 都已经完成原生验收：

| 运行平台族 | 热更新路径 | 跨进程所有权恢复 | 支持声明门槛 |
|---|---|---|---|
| Linux/systemd 或 OpenRC/直接 supervisor | root-only Unix 管理 socket 与增量 `ip` 路由 | 普通 peer 路由随 TUN 消失；自动策略路由切换需重启 | amd64 特权生命周期；arm64 需原生或全系统模拟后才能声明 runtime-verified |
| OpenWrt ARM64/x86_64 | 由 procd reload 调用同一 Linux runtime | 同一 TUN 边界 | 两个实际安装的 APK 必须分别通过 QEMU reload/reboot/traffic fixture |
| FreeBSD/OPNsense | Unix socket 与增量 `route` 操作 | root-only、带校验和的外层 endpoint 路由账本；peer 路由随 TUN 消失 | rc.d/configd 与每条 FreeBSD release train 分开验收 |
| Windows amd64/arm64 | ACL 保护的 named pipe、typed core 事务和 IP Helper peer 路由 | endpoint 账本，加按 compartment/interface LUID 标识的每隧道 before/after/phase 日志 | x64 安装态 SCM/MSI 生命周期；arm64 在通过原生服务 fixture 前仅为 build/unit |

当前开发树已经包含这四类适配器。Release notes 必须针对准确的 OS/架构使用
`build-supported`、`unit-verified`、`runtime-verified` 或
`integration-verified`；仅交叉编译绝不能提升支持等级。

数据面子进程的所有权不依赖 systemd。Linux/OpenWrt 与 FreeBSD/OPNsense 上，
quick 会把一个私有的继承生命周期管道交给 core；即使使用 OpenRC、procd、
rc.d 或直接 supervisor，quick 被杀后管道也会关闭，core 随即退出并释放 TUN。
FreeBSD 另有 parent-death signal 作为双重保护，Windows 则使用
kill-on-close Job Object。服务管理器的 cgroup/process group 仍是额外防线。

当前开发二进制已经在 OpenWrt 25.12.5 的 `armsr/armv8` 与 `x86/64` QEMU
guest 中通过 `runtime-smoke`：包括 TUN 创建、procd 生命周期、hooks 顺序、peer
增删、generation 推进以及 supervisor epoch 保持不变。该轮验证把本地构建的
二进制安装到包路径，并未安装新构建的 APK，因此每个 target 的 APK
安装/流量/重启 fixture 仍是正式 Release 支持声明的门槛。

## 创建第一个配置

在两个 peer 上分别生成密钥对，并妥善保护私钥：

```sh
umask 077
wg-quic genkey > private.key
wg-quic pubkey < private.key > public.key
```

peer A 的最小配置示例：

```ini
[Interface]
PrivateKey = PEER_A_PRIVATE_KEY
Address = 10.203.0.1/32
ListenPort = 51820
MTU = 1280

[Peer]
PublicKey = PEER_B_PUBLIC_KEY
AllowedIPs = 10.203.0.2/32
Endpoint = PEER_B_PUBLIC_IP_OR_NAME:51820
PersistentKeepalive = 25
```

peer B 使用自己的私钥、`10.203.0.2/32`、peer A 的公钥、
`10.203.0.1/32` 和 peer A 可访问的端点。建议先使用精确的 `/32`
`AllowedIPs` 验证分流隧道，再尝试默认路由。如果使用 `PresharedKey`，两端对应
`[Peer]` 中必须填写相同的值。

默认传输配置已启用 QUIC、自适应 FEC 和 Salamander 混淆，因此基础配置不需要
额外传输密码。Salamander 密钥由 WireGuard 密钥协商结果派生；存在 WireGuard
预共享密钥时也会将其混入派生过程。

各平台配置路径：

| 平台 | 配置文件位置 |
|---|---|
| Linux、OpenWrt | `/etc/wg-quic/<名称>.conf` |
| FreeBSD | `/usr/local/etc/wg-quic/<名称>.conf` |
| Windows | `%ProgramData%\wg-quic\interfaces\<名称>.conf` |
| OPNsense | `/usr/local/etc/wg-quic/quicN.conf`，由插件生成 |

Unix 类系统应限制目录和配置文件权限：

```sh
sudo install -d -m 0700 /etc/wg-quic
sudo install -m 0600 wg0.conf /etc/wg-quic/wg0.conf
sudo wg-quic-quick check wg0
```

## Linux

从 [Releases](https://github.com/RC-CHN/wg-quic/releases) 下载与架构匹配的包。
以下以 amd64 为例：

```sh
curl -LO https://github.com/RC-CHN/wg-quic/releases/download/v0.3.1/wg-quic-v0.3.1-linux-amd64.tar.gz
curl -LO https://github.com/RC-CHN/wg-quic/releases/download/v0.3.1/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf wg-quic-v0.3.1-linux-amd64.tar.gz
cd wg-quic-v0.3.1-linux-amd64

sudo install -m 0755 wg-quic wg-quic-quick /usr/local/bin/
sudo install -m 0644 wg-quic@.service /etc/systemd/system/
sudo systemctl daemon-reload
```

把 `wg0.conf` 放入 `/etc/wg-quic/` 后启动；如需开机启动，再启用 unit：

```sh
sudo wg-quic-quick check wg0
sudo wg-quic-quick up wg0
sudo systemctl enable wg-quic@wg0.service
wg-quic-quick show wg0
sudo wg-quic-quick down wg0
```

Linux amd64 桌面用户也可以安装 Deb：

```sh
sudo apt install ./wg-quic-desktop-v0.3.1-linux-amd64.deb
```

桌面端会以 `0600` 权限把配置导入 `/etc/wg-quic/`，仅对固定特权操作调用
`pkexec`。它只是同一套 `wg-quic-quick` 服务和配置模型的 UI，不是另一套隧道
实现。

Linux 需要可用的 TUN 设备和 `CAP_NET_ADMIN`。随包提供的 systemd unit 会授予
所需 capability 和 `/dev/net/tun` 访问权限。

运行时本身不依赖 systemd。使用 OpenRC、runit、s6、dinit、SysV 或自定义
supervisor 时，以 root 运行 `wg-quic-quick run wg0`，然后仍通过同一 root-only
Unix socket 使用 `show`、`reload`、`reconcile` 和 `refresh-endpoints`。systemd
只是生命周期适配器之一，它的 `ExecReload` 同样调用与管理器无关的 CLI。
v0.3.1 之后由当前开发树构建的 Linux 压缩包也包含 OpenRC 实例脚本：

```sh
sudo install -m 0755 wg-quic.openrc /etc/init.d/wg-quic
sudo ln -s wg-quic /etc/init.d/wg-quic.wg0
sudo rc-update add wg-quic.wg0 default
sudo rc-service wg-quic.wg0 start
sudo rc-service wg-quic.wg0 reload
```

使用 runit/s6/dinit 时，让它以前台方式监管 `wg-quic-quick run wg0`，并在服务
控制钩子中调用 `wg-quic-quick reload wg0 --json`。supervisor 必须以 root
运行（或提供等价的 TUN/网络策略权限）、转发终止信号，并且只在进程真正失败时
重启；reload 返回 `restart_required` 时不能擅自重启。

## Windows

x64 Windows 推荐从 [Releases](https://github.com/RC-CHN/wg-quic/releases)
安装 `wg-quic-desktop-v0.3.1-windows-x64.msi`。该 per-machine MSI 只在安装时
请求一次提升权限，将 UI 安装到 Program Files，并注册受限的
`wg-quic-manager` LocalSystem 服务。在桌面应用中使用 **Import** 导入配置，
然后从隧道列表启动或停止。

桌面程序本身保持非提升运行。本地 Administrator 账户可通过已认证管理服务操作
不含 hooks 的配置；标准用户以及包含生命周期 hooks 的配置会回退到单次 UAC
helper。

只使用 CLI 时，下载 amd64 或 arm64 ZIP，并把 `wg-quic.exe`、
`wg-quic-quick.exe` 和对应架构的已签名 `wintun.dll` 放在一起。在提升权限的
PowerShell 中执行：

```powershell
New-Item -ItemType Directory -Force "$env:ProgramData\wg-quic\interfaces"
Copy-Item .\wg0.conf "$env:ProgramData\wg-quic\interfaces\wg0.conf"
.\wg-quic-quick.exe check wg0
.\wg-quic-quick.exe up wg0
.\wg-quic-quick.exe show wg0
.\wg-quic-quick.exe down wg0
```

使用 `wg-quic-quick.exe debug wg0` 进行前台诊断，脱敏日志位于
`%ProgramData%\wg-quic\logs`。如果中断的停止操作遗留了可精确确认归属的服务、
网卡或路由租约，可使用显式恢复命令：

```powershell
.\wg-quic-quick.exe down wg0 --repair
```

作为服务端接收入站会话时，需要在 Windows Firewall 中允许外层 UDP
`ListenPort`。完整局域网测试和恢复流程见
[`packaging/windows/README.md`](packaging/windows/README.md)。

## FreeBSD

下载 amd64 或 arm64 FreeBSD 压缩包，安装两个程序和 rc.d 脚本：

```sh
tar -xzf wg-quic-v0.3.1-freebsd-amd64.tar.gz
cd wg-quic-v0.3.1-freebsd-amd64
install -m 0755 wg-quic wg-quic-quick /usr/local/bin/
install -m 0755 wg_quic /usr/local/etc/rc.d/wg_quic
install -d -m 0700 /usr/local/etc/wg-quic
install -m 0600 /path/to/wg0.conf /usr/local/etc/wg-quic/wg0.conf
```

启用一个或多个接口并启动服务：

```sh
sysrc wg_quic_enable=YES
sysrc 'wg_quic_interfaces=wg0'
service wg_quic start
wg-quic-quick show wg0
service wg_quic stop
```

安装 rc.d 脚本后，`wg-quic-quick up wg0` 和 `wg-quic-quick down wg0` 会使用
同一服务边界。

FreeBSD 已实现全部四种 hooks：`PreUp`、`PostUp`、`PreDown`、`PostDown`。
它们通过 `/bin/sh -c` 执行，`%i` 会替换为接口名；rc.d 以 root 运行时 hooks
也具有 root 权限。正常清理顺序为 `PreDown`、撤销网络配置、`PostDown`。
强制 `SIGKILL` 或断电无法保证执行清理 hooks。

## OPNsense 26.1 和 26.7

必须使用与 OPNsense 版本完全匹配的软件包：

- `os-wg-quic-0.3.1-opnsense-26.1-amd64.pkg`
- `os-wg-quic-0.3.1-opnsense-26.7-amd64.pkg`

将软件包复制到防火墙后，通过控制台或 SSH 安装。OPNsense 26.7 示例：

```sh
pkg add -f /tmp/os-wg-quic-0.3.1-opnsense-26.7-amd64.pkg
```

然后打开 `VPN > wg-quic`：

1. 添加或生成 peers。
2. 创建实例并分配所需 peer。
3. 应用配置并查看 **Status** 页面。
4. 在 `wg-quic (Group)` 上为所需内层流量添加明确的 pass 规则。与其他新建
   OPNsense VPN 接口相同，仅安装插件不会自动放行流量。

插件负责 `quicN` 接口、生成的配置、configd actions、服务生命周期、
CARP/XML-RPC、API、日志和 Dashboard 小组件，并与 OPNsense 内置 WireGuard
集成相互独立。

OPNsense 底层同样使用 FreeBSD `wg-quic-quick`，因此运行层支持 hooks；但当前
Web UI 没有提供任意 `PreUp/PostUp/PreDown/PostDown` 输入字段，手工修改生成的
`quicN.conf` 也会被模板覆盖。

默认禁用路由安装。远程启用宽泛 `AllowedIPs` 前，应确认它不会替换管理防火墙所
依赖的路由，并保留控制台或其他恢复路径。使用 `pkg delete os-wg-quic` 卸载。

构建和 QEMU 细节见
[`wg-quic-opnsense/README.md`](wg-quic-opnsense/README.md)。

## OpenWrt 25.12.5

当前 OpenWrt workflow 构建两个 64 位目标：

- `armsr/armv8`，包架构为 `aarch64_generic`；
- `x86/64`，包架构为 `x86_64`。

可以从
[`openwrt-package` workflow](https://github.com/RC-CHN/wg-quic/actions/workflows/openwrt-package.yml)
下载匹配的 artifact，也可以使用固定官方 SDK 的脚本构建：

```sh
./packaging/openwrt/build-release-target.sh arm64
./packaging/openwrt/build-release-target.sh x86_64
```

不要安装为其他 OpenWrt 版本或 target 构建的 APK。`kmod-tun` 等内核包必须与
当前固件完全匹配。在路由器上安装对应 APK：

```sh
apk add --allow-untrusted ./wg-quic-0.3.1-r1-openwrt-25.12.5-armsr-armv8.apk
```

软件包依赖 `kmod-tun` 和 `ip-full`，安装两个可执行程序并注册 procd 多实例
服务。配置位于 `/etc/wg-quic/`：

```sh
install -d -m 0700 /etc/wg-quic
chmod 0600 /etc/wg-quic/aws.conf
wg-quic-quick check aws
wg-quic-quick up aws
wg-quic-quick show aws --json
wg-quic-quick down aws
```

如需开机启动，添加一个 UCI 实例：

```sh
uci set wg-quic.aws='instance'
uci set wg-quic.aws.enabled='1'
uci set wg-quic.aws.config='/etc/wg-quic/aws.conf'
uci commit wg-quic
/etc/init.d/wg-quic enable
/etc/init.d/wg-quic reload
```

如果 `/dev/net/tun` 不存在，应先确认 APK 与固件 target 完全匹配，并检查依赖
是否成功安装：

```sh
apk add kmod-tun ip-full
test -c /dev/net/tun
```

不要用底层 `wg-quic run` 绕过此问题；它仍然不会配置地址、路由或 hooks。
应使用 `wg-quic-quick` 或 procd。

OpenWrt 通常没有 `systemd-resolved` 或 `resolvconf`。目前应在 OpenWrt
network/dnsmasq 中管理 DNS，不要在 profile 中添加 `DNS =`。隧道流量规则优先
写入持久化 `/etc/config/firewall`。通过 quick/procd 监督路径支持生命周期
`PostUp`/`PostDown`，hooks 以 root 执行，因此配置必须保持 `0600` 并仔细审查
每条命令。

UCI/procd 和脱敏 JSON 状态接口已为未来 LuCI 应用准备好，但**当前尚未包含
LuCI UI**。防火墙 hooks、SDK 和打包细节见
[`packaging/openwrt/README.md`](packaging/openwrt/README.md)，ARM64 与 x86_64
QEMU 流程见 [`tests/openwrt/README.md`](tests/openwrt/README.md)。

## 架构和协议说明

固定版本的用户态 WireGuard 实现在
[`third_party/wireguard-go`](third_party/wireguard-go)，完整固定版本的 quic-go
源码在 [`third_party/quic-go`](third_party/quic-go)。生产构建不会从 module
cache 下载或替换这两个实现。

`wg-quic-quick` 只解析和验证配置一次，解析主机策略后，通过标准输入把不可变
配置快照传给受监督的 core。私钥和预共享密钥不会进入进程参数，受监督 core
也不会重新读取配置路径。

线格式、传输指令、安全边界、自适应 FEC 策略和当前限制见
[`docs/WG-QUIC-PROTOCOL.md`](docs/WG-QUIC-PROTOCOL.md)。

## 开发和验证

常规本地检查：

```sh
go test ./...
make test-wireguard
make test-transport
make test-quic
./tests/container/test.sh
make build
```

特权容器 fixture 将 TUN、路由、DNS 和网络模拟限制在隔离的 Linux namespace
中。CI 覆盖 IPv4/IPv6 内外层路径、TCP/UDP、大包、MTU、丢包/FEC、乱序、NAT
重绑定和 peer 重启恢复。固定 WireGuard 测试映射见
[`tests/WIREGUARD-FORK.md`](tests/WIREGUARD-FORK.md)。

性能和可控丢包测试：

```sh
make benchmark-smoke
make benchmark-transports
make benchmark-ceiling
make benchmark-loss
make benchmark-profiles
make benchmark-bandwidth
make benchmark-protocol
```

LAN、Wi-Fi、蜂窝、卫星、FEC、混淆、CPU 和协议特征测试说明见
[`tests/benchmark/README.md`](tests/benchmark/README.md)。

Windows/Linux 桌面端源码与测试位于 [`desktop/`](desktop/README.md)，OPNsense
插件位于 [`wg-quic-opnsense/`](wg-quic-opnsense/README.md) monorepo 子目录。

## Release 归档

每个 `v*` tag 都必须通过 Linux、Windows、FreeBSD、Linux 特权隧道矩阵、桌面
安装包、OPNsense 包和 OpenWrt 包矩阵。Release job 会在顶层 `SHA256SUMS`
中发布所有校验和。

根目录的 [`VERSION`](VERSION) 是发布版本的唯一来源。打包脚本和手动触发的
Release workflow 会自动读取它；可选的 workflow 输入只用于断言请求版本与它
一致。桌面端 npm、Cargo 和 Tauri 的原生工具仍要求各自 manifest 使用字面版本，
`npm run version:check --prefix desktop` 会检查它们是否发生漂移。

本地构建并验证六个便携 CLI 压缩包：

```sh
make release-artifacts VERSION=0.3.1
./scripts/check-release-archive.sh \
  dist/wg-quic-v0.3.1-linux-amd64.tar.gz linux amd64 0.3.1
```

OpenWrt 和 OPNsense 包还必须匹配具体固件版本和打包框架；CPU 架构相同并不足以
保证兼容。
