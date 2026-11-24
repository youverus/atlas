import { useEffect, useRef, useState } from "react";
import { api, type DeviceInfo, type DeviceInput } from "../api";

/**
 * Modal dialog for adding or editing a remote SSH compute host. Can be opened
 * either from Settings -> 远程设备 or inline from ProjectDialog.
 */
export function DeviceDialog({
  open,
  initialDevice,
  sshHosts,
  onSaved,
  onClose,
}: {
  open: boolean;
  initialDevice: DeviceInfo | null;
  sshHosts: string[];
  onSaved: (device: DeviceInfo, connectNow: boolean) => Promise<void> | void;
  onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const [user, setUser] = useState("");
  const [identityFile, setIdentityFile] = useState("");
  const [remoteBin, setRemoteBin] = useState("");
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const hostInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    if (initialDevice) {
      setName(initialDevice.name);
      setHost(initialDevice.host);
      setPort(initialDevice.port ? String(initialDevice.port) : "");
      setUser(initialDevice.user ?? "");
      setIdentityFile(initialDevice.identityFile ?? "");
      setRemoteBin(initialDevice.remoteBin ?? "");
      setShowAdvanced(Boolean(initialDevice.identityFile || initialDevice.remoteBin));
    } else {
      setName("");
      setHost("");
      setPort("");
      setUser("");
      setIdentityFile("");
      setRemoteBin("");
      setShowAdvanced(false);
    }
    setError("");
    setSubmitting(false);
    const frame = window.requestAnimationFrame(() => {
      hostInputRef.current?.focus();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [open, initialDevice]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !submitting) {
        onClose();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, submitting, onClose]);

  if (!open) return null;

  const submitDevice = async (connectNow: boolean) => {
    const cleanHost = host.trim();
    if (!cleanHost) {
      setError("请填写 SSH Host 名称、域名或 IP。");
      return;
    }
    let parsedPort = 0;
    if (port.trim()) {
      parsedPort = Number.parseInt(port.trim(), 10);
      if (!Number.isFinite(parsedPort) || parsedPort <= 0 || parsedPort > 65535) {
        setError("端口号必须是 1 到 65535 之间的整数。");
        return;
      }
    }

    const input: DeviceInput = {
      name: name.trim() || cleanHost,
      host: cleanHost,
      ...(parsedPort > 0 ? { port: parsedPort } : {}),
      ...(user.trim() ? { user: user.trim() } : {}),
      ...(identityFile.trim() ? { identityFile: identityFile.trim() } : {}),
      ...(remoteBin.trim() ? { remoteBin: remoteBin.trim() } : {}),
    };

    setSubmitting(true);
    setError("");
    try {
      const saved = initialDevice
        ? await api.updateDevice(initialDevice.id, input)
        : await api.createDevice(input);
      if (connectNow) {
        const connected = await api.connectDevice(saved.id);
        await onSaved(connected, true);
      } else {
        await onSaved(saved, false);
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      className="sheet-backdrop"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !submitting) onClose();
      }}
    >
      <div
        className="sheet sheet--narrow project-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={initialDevice ? "编辑远程设备" : "添加远程设备"}
      >
        <div className="sheet__head">
          <span className="sheet__title">{initialDevice ? "编辑远程设备" : "添加远程设备"}</span>
          <button
            type="button"
            className="icon-button"
            onClick={onClose}
            disabled={submitting}
            aria-label="关闭"
          >
            ✕
          </button>
        </div>

        <form
          className="sheet__body"
          onSubmit={(event) => {
            event.preventDefault();
            void submitDevice(true);
          }}
        >
          {error ? <div className="notice notice--error project-dialog__error">{error}</div> : null}

          {!initialDevice && sshHosts.length > 0 ? (
            <div className="field">
              <div className="field__label">从 ~/.ssh/config 快速填入</div>
              <div className="device-dialog__ssh-picks">
                {sshHosts.slice(0, 12).map((alias) => (
                  <button
                    key={alias}
                    type="button"
                    className="device-dialog__ssh-chip"
                    data-active={host.trim() === alias ? "true" : undefined}
                    disabled={submitting}
                    onClick={() => {
                      setHost(alias);
                      if (!name.trim()) setName(alias);
                    }}
                  >
                    {alias}
                  </button>
                ))}
              </div>
            </div>
          ) : null}

          <div className="field">
            <label className="field__label" htmlFor="device-host-input">
              SSH Host / 地址
            </label>
            <input
              id="device-host-input"
              ref={hostInputRef}
              className="field__input"
              value={host}
              placeholder="ssh-alias 或 10.0.0.8"
              disabled={submitting}
              onChange={(event) => setHost(event.target.value)}
            />
            <div className="field__hint">直接使用本机免密 SSH 配置（密钥、ProxyJump、Agent）。</div>
          </div>

          <div className="field">
            <label className="field__label" htmlFor="device-name-input">
              显示名称
            </label>
            <input
              id="device-name-input"
              className="field__input"
              value={name}
              placeholder={host.trim() || "GPU 训练机"}
              disabled={submitting}
              onChange={(event) => setName(event.target.value)}
            />
          </div>

          <div className="device-dialog__row">
            <div className="field">
              <label className="field__label" htmlFor="device-user-input">
                用户名（可选）
              </label>
              <input
                id="device-user-input"
                className="field__input"
                value={user}
                placeholder="默认读取 SSH 配置"
                disabled={submitting}
                onChange={(event) => setUser(event.target.value)}
              />
            </div>

            <div className="field device-dialog__port-field">
              <label className="field__label" htmlFor="device-port-input">
                端口（可选）
              </label>
              <input
                id="device-port-input"
                className="field__input"
                value={port}
                placeholder="22"
                inputMode="numeric"
                disabled={submitting}
                onChange={(event) => setPort(event.target.value)}
              />
            </div>
          </div>

          <div className="field">
            <button
              type="button"
              className="project-dialog__link"
              disabled={submitting}
              onClick={() => setShowAdvanced((v) => !v)}
            >
              {showAdvanced ? "▾ 收起高级选项" : "▸ 高级选项（私钥 / 远程二进制路径）"}
            </button>
          </div>

          {showAdvanced ? (
            <>
              <div className="field">
                <label className="field__label" htmlFor="device-key-input">
                  SSH 私钥路径（可选）
                </label>
                <input
                  id="device-key-input"
                  className="field__input"
                  value={identityFile}
                  placeholder="~/.ssh/id_ed25519"
                  disabled={submitting}
                  onChange={(event) => setIdentityFile(event.target.value)}
                />
              </div>
              <div className="field">
                <label className="field__label" htmlFor="device-bin-input">
                  远程 atlas 二进制路径（可选）
                </label>
                <input
                  id="device-bin-input"
                  className="field__input"
                  value={remoteBin}
                  placeholder="留空则自动探测或部署到 ~/.atlas/bin/atlas"
                  disabled={submitting}
                  onChange={(event) => setRemoteBin(event.target.value)}
                />
              </div>
            </>
          ) : null}

          <div className="sheet__actions">
            <button type="button" className="button" onClick={onClose} disabled={submitting}>
              取消
            </button>
            <button
              type="button"
              className="button"
              disabled={submitting || !host.trim()}
              onClick={() => void submitDevice(false)}
            >
              仅保存
            </button>
            <button
              type="submit"
              className="button button--primary"
              disabled={submitting || !host.trim()}
            >
              {submitting ? "连接中…" : "保存并连接"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
