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
- 部署模式: 节点模式、机器模式、独立模式
- 多实例: 单进程绑定多个面板/节点
- 远程操作: 面板端远程升级和重启

## 安装

### 安装脚本（Linux systemd）

```bash
# 节点模式
curl -fsSL https://raw.githubusercontent.com/Fearless743/fboard-node/dev/install.sh | \
  sudo bash -s -- --mode node --panel https://panel.example.com --token TOKEN --node-id 1

# 机器模式
curl -fsSL https://raw.githubusercontent.com/Fearless743/fboard-node/dev/install.sh | \
  sudo bash -s -- --mode machine --panel https://panel.example.com --token TOKEN --machine-id 1
```

### fbctl

安装后运行 `fbctl` 获取帮助：

```bash
fbctl list                          # 列出所有实例
fbctl status                        # 运行状态
fbctl service restart               # 重启服务
fbctl upgrade                       # 升级二进制
fbctl bind add-node --panel URL --token TOKEN --node-id 1
fbctl bind add-machine --panel URL --token TOKEN --machine-id 1
fbctl bind remove-node --panel URL --node-id 1
```

### 手动构建

```bash
git clone https://github.com/Fearless743/fboard-node.git
cd fboard-node/Fboard-Node
make build
sudo cp fboard-node /usr/local/bin/
sudo cp fbctl /usr/local/bin/
sudo mkdir -p /etc/fboard-node
sudo cp config.yml.example /etc/fboard-node/config.yml
# 编辑 /etc/fboard-node/config.yml
sudo fboard-node -c /etc/fboard-node/config.yml
```

## 配置

传统单面板配置完全兼容。追加 bindings 自动迁移到 `instances` 格式。详见 `config.yml.example`。

## 扩展

- 自定义路由: [docs-custom-routes.md](docs-custom-routes.md)
- 自定义出站: [docs-custom-outbounds.md](docs-custom-outbounds.md)
- DNS 供应商（ACME DNS-01）: [docs-dns-providers.md](docs-dns-providers.md)

## 远程升级与重启

面板管理员可通过后台界面向节点推送升级或重启指令：

- **升级**: 面板发送 `sync.upgrade` 事件，节点收到后自动执行 `fbctl upgrade --version <version>`
- **重启**: 面板发送 `sync.restart` 事件，节点收到后优雅停止内核并退出进程（systemd 自动拉起）

## License

MPL-2.0. Based on [cedar2025/Xboard-Node](https://github.com/cedar2025/Xboard-Node).
