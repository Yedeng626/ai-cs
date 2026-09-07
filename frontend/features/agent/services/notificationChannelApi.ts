import { apiUrl, getAgentHeaders } from "@/lib/config";

export type NotifyPlatform = "dingtalk" | "feishu" | "wecom";
export type NotifyKind = "group" | "supervisor";

export interface NotificationChannel {
  id?: number;
  platform: NotifyPlatform;
  kind: NotifyKind;
  webhook_url: string;
  secret: string;
  enabled: boolean;
}

export interface NotificationChannelsResult {
  channels: NotificationChannel[];
  env: {
    group_url_set: boolean;
    supervisor_url_set: boolean;
  };
}

export async function fetchNotificationChannels(): Promise<NotificationChannelsResult> {
  const res = await fetch(apiUrl("/agent/notification-channels"), {
    cache: "no-store",
    headers: getAgentHeaders(),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || "获取通知渠道配置失败");
  }
  return res.json();
}

export async function upsertNotificationChannel(
  data: NotificationChannel
): Promise<NotificationChannelsResult> {
  const res = await fetch(apiUrl("/agent/notification-channels"), {
    method: "PUT",
    headers: { "Content-Type": "application/json", ...getAgentHeaders() },
    body: JSON.stringify(data),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || "保存通知渠道失败");
  }
  return res.json();
}

export async function sendNotificationChannelTest(
  data: Pick<NotificationChannel, "platform" | "webhook_url" | "secret"> & {
    content?: string;
  }
): Promise<void> {
  const res = await fetch(apiUrl("/agent/notification-channels/test"), {
    method: "POST",
    headers: { "Content-Type": "application/json", ...getAgentHeaders() },
    body: JSON.stringify(data),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || "发送测试消息失败");
  }
}
