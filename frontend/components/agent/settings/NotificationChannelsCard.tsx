"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { toast } from "@/hooks/useToast";
import {
  fetchNotificationChannels,
  upsertNotificationChannel,
  sendNotificationChannelTest,
  type NotificationChannel,
  type NotifyPlatform,
  type NotifyKind,
} from "@/features/agent/services/notificationChannelApi";

const PLATFORMS: { value: NotifyPlatform; label: string; color: string }[] = [
  { value: "dingtalk", label: "钉钉", color: "bg-[#0089FF]/10 text-[#0089FF] border-[#0089FF]/30" },
  { value: "feishu", label: "飞书", color: "bg-[#3370FF]/10 text-[#3370FF] border-[#3370FF]/30" },
  { value: "wecom", label: "企业微信", color: "bg-[#2BA245]/10 text-[#2BA245] border-[#2BA245]/30" },
];

const KINDS: { value: NotifyKind; label: string; hint: string }[] = [
  { value: "group", label: "群通知", hint: "新人工咨询/派单/无客服在线/客服超时 推送到群" },
  { value: "supervisor", label: "主管通知", hint: "两轮派单均无人接入时推送主管" },
];

const keyOf = (p: NotifyPlatform, k: NotifyKind) => `${p}__${k}`;

interface RowState {
  platform: NotifyPlatform;
  kind: NotifyKind;
  enabled: boolean;
  webhook_url: string;
  secret: string;
  dirty: boolean;
}

export function NotificationChannelsCard({ isAdmin }: { isAdmin: boolean }) {
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testingKey, setTestingKey] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [envGroupSet, setEnvGroupSet] = useState(false);
  const [envSupervisorSet, setEnvSupervisorSet] = useState(false);

  const emptyRows = useMemo(() => {
    const map = new Map<string, RowState>();
    for (const p of PLATFORMS) {
      for (const k of KINDS) {
        map.set(keyOf(p.value, k.value), {
          platform: p.value,
          kind: k.value,
          enabled: false,
          webhook_url: "",
          secret: "",
          dirty: false,
        });
      }
    }
    return map;
  }, []);

  const [rows, setRows] = useState<Map<string, RowState>>(emptyRows);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await fetchNotificationChannels();
      const next = new Map(emptyRows);
      for (const ch of data.channels) {
        const key = keyOf(ch.platform as NotifyPlatform, ch.kind as NotifyKind);
        const existing = next.get(key);
        if (existing) {
          next.set(key, {
            ...existing,
            enabled: ch.enabled,
            webhook_url: ch.webhook_url || "",
            secret: ch.secret || "",
          });
        }
      }
      setRows(next);
      setEnvGroupSet(data.env?.group_url_set ?? false);
      setEnvSupervisorSet(data.env?.supervisor_url_set ?? false);
    } catch (e) {
      setError((e as Error).message || "加载通知渠道配置失败");
    } finally {
      setLoading(false);
    }
  }, [emptyRows]);

  useEffect(() => {
    void load();
  }, [load]);

  const updateRow = (key: string, patch: Partial<RowState>) => {
    setRows((prev) => {
      const next = new Map(prev);
      const row = next.get(key);
      if (row) {
        next.set(key, { ...row, ...patch, dirty: true });
      }
      return next;
    });
  };

  const hasAnyEnabled = useMemo(() => {
    for (const row of rows.values()) {
      if (row.enabled && row.webhook_url.trim()) return true;
    }
    return false;
  }, [rows]);

  const handleSaveAll = async () => {
    setSaving(true);
    setError("");
    try {
      // 只保存改动过的行（避免覆盖用户未触碰的历史配置）
      const dirtyRows = [...rows.values()].filter((r) => r.dirty);
      if (dirtyRows.length === 0) {
        toast({ title: "没有需要保存的改动" });
        setSaving(false);
        return;
      }
      for (const row of dirtyRows) {
        await upsertNotificationChannel({
          platform: row.platform,
          kind: row.kind,
          webhook_url: row.webhook_url.trim(),
          secret: row.secret.trim(),
          enabled: row.enabled,
        });
      }
      toast.success("通知渠道已保存，立即生效");
      // 重置 dirty 标记
      setRows((prev) => {
        const next = new Map(prev);
        for (const [k, v] of next) {
          next.set(k, { ...v, dirty: false });
        }
        return next;
      });
      void load(); // 刷新以显示最新（含 upsert 返回）
    } catch (e) {
      setError((e as Error).message || "保存失败");
      toast.error((e as Error).message || "保存失败");
    } finally {
      setSaving(false);
    }
  };

  const handleTest = async (row: RowState) => {
    if (!row.webhook_url.trim()) {
      toast.error("请先填写 Webhook 地址再测试");
      return;
    }
    setTestingKey(keyOf(row.platform, row.kind));
    setError("");
    try {
      await sendNotificationChannelTest({
        platform: row.platform,
        webhook_url: row.webhook_url.trim(),
        secret: row.secret.trim(),
        content: "AI-CS 消息通知渠道测试 ✅",
      });
      toast.success("测试消息已发送，请检查群机器人是否收到");
    } catch (e) {
      const msg = (e as Error).message || "发送失败";
      setError(msg);
      toast.error(msg);
    } finally {
      setTestingKey(null);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>消息通知渠道</CardTitle>
        <p className="text-sm text-muted-foreground mt-1">
          新人工咨询、客服派单、超时告警等事件推送到群机器人，支持钉钉 / 飞书 / 企业微信。保存后立即生效。
        </p>
        {!isAdmin ? (
          <p className="text-xs text-amber-600 mt-2">仅管理员可修改</p>
        ) : null}
        {(envGroupSet || envSupervisorSet) && (
          <p className="text-xs text-muted-foreground mt-1">
            环境变量兜底：{envGroupSet ? "DINGTALK_WEBHOOK_URL（群）" : ""}
            {envGroupSet && envSupervisorSet ? "、" : ""}
            {envSupervisorSet ? "DINGTALK_SUPERVISOR_WEBHOOK_URL（主管）" : ""}
            {" "}已配置，未启用下方渠道时自动使用。
          </p>
        )}
      </CardHeader>
      <CardContent>
        {loading ? (
          <div className="text-center py-6 text-muted-foreground">加载中...</div>
        ) : (
          <div className="space-y-6">
            {error && (
              <div className="p-3 bg-red-50 border border-red-200 rounded-md text-red-600 text-sm">
                {error}
              </div>
            )}

            {PLATFORMS.map((platform) => (
              <div
                key={platform.value}
                className="rounded-lg border border-slate-200 p-4 space-y-3"
              >
                <div className="flex items-center gap-2">
                  <span
                    className={`inline-flex items-center rounded-md border px-2 py-0.5 text-xs font-medium ${platform.color}`}
                  >
                    {platform.label}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    {platform.value === "dingtalk" && "需「安全设置-自定义关键词/加签」按群机器人文档配置"}
                    {platform.value === "feishu" && "自定义机器人，可选加签"}
                    {platform.value === "wecom" && "群机器人，无需加签"}
                  </span>
                </div>

                {KINDS.map((kind) => {
                  const key = keyOf(platform.value, kind.value);
                  const row = rows.get(key);
                  if (!row) return null;
                  const testing = testingKey === key;
                  return (
                    <div
                      key={kind.value}
                      className="space-y-2 border-t border-slate-100 pt-3"
                    >
                      <div className="flex items-center gap-2">
                        <Checkbox
                          id={`${key}-enabled`}
                          checked={row.enabled}
                          disabled={!isAdmin}
                          onCheckedChange={(checked) =>
                            updateRow(key, { enabled: checked === true })
                          }
                        />
                        <Label
                          htmlFor={`${key}-enabled`}
                          className="text-sm font-medium cursor-pointer"
                        >
                          {kind.label}
                        </Label>
                        <span className="text-xs text-muted-foreground">
                          {kind.hint}
                        </span>
                      </div>
                      <div className="grid grid-cols-1 md:grid-cols-[1fr_240px_auto] gap-2">
                        <Input
                          value={row.webhook_url}
                          disabled={!isAdmin}
                          placeholder="Webhook 地址 https://..."
                          onChange={(e) =>
                            updateRow(key, { webhook_url: e.target.value })
                          }
                        />
                        <Input
                          value={row.secret}
                          disabled={!isAdmin}
                          placeholder={
                            platform.value === "wecom"
                              ? "企业微信无需加签"
                              : "加签密钥（可选）"
                          }
                          onChange={(e) =>
                            updateRow(key, { secret: e.target.value })
                          }
                        />
                        <Button
                          type="button"
                          variant="outline"
                          disabled={!isAdmin || testing}
                          onClick={() => void handleTest(row)}
                        >
                          {testing ? "发送中..." : "发送测试"}
                        </Button>
                      </div>
                    </div>
                  );
                })}
              </div>
            ))}

            <div className="flex items-center gap-3 pt-1">
              <Button
                type="button"
                disabled={!isAdmin || saving}
                onClick={() => void handleSaveAll()}
              >
                {saving ? "保存中..." : "保存全部渠道"}
              </Button>
              {!hasAnyEnabled && !envGroupSet && !envSupervisorSet ? (
                <span className="text-xs text-amber-600">
                  当前没有任何启用渠道，客服事件将无人接收
                </span>
              ) : null}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
