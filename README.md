# fboard-node

Node backend for [Fboard](https://github.com/Fearless743/Fboard). Based on [Xboard-Node](https://github.com/cedar2025/Xboard-Node) by cedar2025.

> **⚠️ 二改声明**
>
> 本仓库（fboard-node）是基于 [cedar2025/Xboard-Node](https://github.com/cedar2025/Xboard-Node) 的二次修改版本。
> 原项目采用 MPL-2.0 许可证，本修改版本同样遵循 MPL-2.0 许可证。
>
> 主要修改：
> - 移除 sing-box / mihomo 双内核，仅保留 xray-core 单内核
> - 在内核 xray-core 中添加 TUIC、AnyTLS、Naive、Mieru 协议支持
> - 内核配置清理（移除内核选择逻辑）
> - 全面重命名为 fboard-node 并指向 [Fearless743/Xray-core](https://github.com/Fearless743/Xray-core)
>
> 感谢 cedar2025 的原始工作。

## 功能

- 协议: VMess、VLESS、Trojan、Shadowsocks、Hysteria2、TUIC、AnyTLS、Naive、Mieru、SOCKS、HTTP
- 内核: xray-core（单内核，已移除 sing-box / mihomo）
- 同步: WebSocket 推送 + REST 轮询双通道
- 用户控制: 限速、设备限制、在线 IP 追踪、热更新
- 部署模式: 机器模式（动态发现绑定节点）
- 多实例: 单进程绑定多个面板机器
- 远程操作: 面板端远程升级和重启

## 安装

预编译产物支持 **Linux / FreeBSD**（`amd64`、`arm64`）。Release 上传 **按平台打包的 `.tar.gz`**（内含 `fboard-node` + `fbctl`）：

| 平台 | 产物示例 |
|------|----------|
| Linux | `fboard-node-linux-amd64.tar.gz` / `fboard-node-linux-arm64.tar.gz` |
| FreeBSD | `fboard-node-freebsd-amd64.tar.gz` / `fboard-node-freebsd-arm64.tar.gz` |

配置与数据目录统一为 `/etc/fboard-node`，二进制安装到 `/usr/local/bin/{fboard-node,fbctl}`。

### 安装脚本（Linux）

需要 root。systemd（Debian/Ubuntu/RHEL 等）与 OpenRC（Alpine 等）均可：

```bash
# 机器模式
curl -fsSL https://raw.githubusercontent.com/Fearless743/fboard-node/dev/install.sh | \
  sudo bash -s -- --mode machine --panel https://panel.example.com --token TOKEN --machine-id 1
```

### 安装脚本（FreeBSD）

FreeBSD 默认没有 bash，请先安装依赖。服务使用 **rc.d**，脚本名为 `fboard_node`（rc 不允许连字符）：

```sh
pkg install -y bash curl ca_root_nss

fetch -o - https://raw.githubusercontent.com/Fearless743/fboard-node/dev/install.sh | \
  bash -s -- --mode machine --panel https://panel.example.com --token TOKEN --machine-id 1

# 服务管理
service fboard_node status
sysrc fboard_node_enable   # 应为 YES
# 日志：/var/log/fboard-node.log
```

### fbctl

安装后运行 `fbctl` 获取帮助：

```bash
fbctl list                          # 列出所有实例
fbctl status                        # 运行状态
fbctl service restart               # 重启服务（Linux: systemd/OpenRC；FreeBSD: service fboard_node）
fbctl upgrade                       # 升级二进制（按 runtime.GOOS 下载对应产物）
fbctl bind add-machine --panel-url URL --token TOKEN --machine-id 1
fbctl bind remove-machine --panel URL --machine-id 1
```

### 手动构建

```bash
git clone https://github.com/Fearless743/fboard-node.git
cd fboard-node
make build                  # 当前平台
make build-linux            # linux/amd64 裸二进制
make build-freebsd          # freebsd/amd64 裸二进制
make build-all              # 四个平台裸二进制
make pack-all               # 四个平台 .tar.gz（发布用，内含 fboard-node + fbctl）

sudo cp fboard-node /usr/local/bin/   # 或对应交叉编译产物
sudo cp fbctl /usr/local/bin/
sudo mkdir -p /etc/fboard-node
sudo cp config.yml.example /etc/fboard-node/config.yml
# 编辑 /etc/fboard-node/config.yml
sudo fboard-node -c /etc/fboard-node/config.yml
```

## 配置

仅支持机器模式配置。详见 `config.yml.example`；旧 node 配置请用 `migrate-from-xboard-node.sh` 迁移。

## 扩展

- 自定义路由: [docs-custom-routes.md](docs-custom-routes.md)
- 自定义出站: [docs-custom-outbounds.md](docs-custom-outbounds.md)
- DNS 供应商（ACME DNS-01）: [docs-dns-providers.md](docs-dns-providers.md)

## 远程升级与重启

面板管理员可通过后台界面向节点推送升级或重启指令：

- **升级**: 面板发送 `sync.upgrade` 事件，节点收到后自动执行 `fbctl upgrade --version <version>`（按本机 OS 下载 `fboard-node-{os}-{arch}.tar.gz` 并解压）
- **重启**: 面板发送进程重启指令后，节点通过本机服务管理器拉起（systemd / OpenRC / FreeBSD `service fboard_node`）；无管理器时回退自重启

## License

MPL-2.0. Based on [cedar2025/Xboard-Node](https://github.com/cedar2025/Xboard-Node).
