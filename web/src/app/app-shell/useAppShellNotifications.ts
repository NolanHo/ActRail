import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { api } from "../../lib/api";
import type { NotificationSubscriptionStateResponse, VoiceSettingsResponse } from "../../lib/types";
import { toPublicAssetUrl } from "../../lib/publicAssetUrl";
import {
  base64UrlToUint8Array,
  isStalePushSubscriptionEndpoint,
  notificationDeviceClass,
  readLocalToggle,
  readLocalToggleDefaultOn,
  replySoundTextKey,
  writeLocalToggle,
} from "./utils";

const NOTIFICATION_MESSAGE_RETRY_MS = 15000;
const FINAL_NOTIFICATION_SUMMARY_STATUSES = new Set(["sent", "skipped", "error"]);
const REPLY_SOUND_TEXT_DEDUPE_MS = 30000;
const NOTIFICATION_FEED_POLL_MS = 5000;
const REALTIME_NOTIFICATION_RECOVERY_MS = 60000;

function isDocumentVisible() {
  if (typeof document === "undefined") {
    return true;
  }
  return document.visibilityState !== "hidden";
}

type NotificationMessageLookupState = {
  retryAfter: number;
  terminal: boolean;
};

type FinalResponseSignature = {
  key: string;
  notificationText: string;
  sessionId: string;
};

interface UseAppShellNotificationsOptions {
  activeSessionId: string | null;
  activeTitle: string;
  bootstrapLoaded?: boolean;
  finalResponseSignatures?: FinalResponseSignature[];
  notificationsSupported?: boolean;
  playReplyBeep(): void;
  realtimeConnected?: boolean;
  suppressedReplySoundSessionIdsRef: { current: Set<string> };
  voiceSettings: VoiceSettingsResponse;
}

export function useAppShellNotifications({
  activeSessionId,
  activeTitle,
  bootstrapLoaded = false,
  finalResponseSignatures = [],
  notificationsSupported = true,
  playReplyBeep,
  realtimeConnected = false,
  suppressedReplySoundSessionIdsRef,
  voiceSettings,
}: UseAppShellNotificationsOptions) {
  const [notificationsEnabled, setNotificationsEnabled] = useState(() => readLocalToggle("actrail.notificationEnabled"));
  const [replySoundEnabled, setReplySoundEnabled] = useState(() => readLocalToggleDefaultOn("actrail.replySoundEnabled"));
  const [notificationPermission, setNotificationPermission] = useState(() => (
    typeof Notification === "undefined" ? "unsupported" : Notification.permission
  ));
  const [pushNotificationsEnabled, setPushNotificationsEnabled] = useState(false);
  const [pageVisible, setPageVisible] = useState(isDocumentVisible);

  const playReplyBeepRef = useRef(playReplyBeep);
  const notificationFeedCursorRef = useRef(Date.now() / 1000);
  const deliveredNotificationIdsRef = useRef(new Set<string>());
  const resolvingNotificationIdsRef = useRef(new Set<string>());
  const notificationLookupStateRef = useRef(new Map<string, NotificationMessageLookupState>());
  const notificationEndpointRef = useRef("");
  const seenFinalResponseKeysRef = useRef(new Set<string>());
  const playedReplySoundKeysRef = useRef(new Set<string>());
  const playedReplySoundTextKeysRef = useRef(new Map<string, number>());
  const finalResponseBeepPrimedRef = useRef(false);

  useEffect(() => {
    playReplyBeepRef.current = playReplyBeep;
  }, [playReplyBeep]);

  useEffect(() => {
    const handleVisibilityChange = () => {
      setPageVisible(isDocumentVisible());
    };
    document.addEventListener("visibilitychange", handleVisibilityChange);
    return () => document.removeEventListener("visibilitychange", handleVisibilityChange);
  }, []);

  useEffect(() => {
    writeLocalToggle("actrail.notificationEnabled", notificationsEnabled);
  }, [notificationsEnabled]);

  useEffect(() => {
    if (typeof window === "undefined") return;
    window.localStorage.setItem("actrail.replySoundEnabled", replySoundEnabled ? "1" : "0");
  }, [replySoundEnabled]);

  const ensureVoiceServiceWorker = async () => {
    if (!("serviceWorker" in navigator)) {
      throw new Error("service workers are not available");
    }
    return navigator.serviceWorker.register(toPublicAssetUrl("service-worker.js"));
  };

  const syncNotificationSubscriptionState = async (
    snapshot?: NotificationSubscriptionStateResponse | null,
    endpointOverride?: string,
  ) => {
    if (!bootstrapLoaded || !notificationsSupported || notificationDeviceClass() !== "mobile" || !("serviceWorker" in navigator) || typeof PushManager === "undefined") {
      notificationEndpointRef.current = "";
      setPushNotificationsEnabled(false);
      return;
    }
    let endpoint = String(endpointOverride || "").trim();
    if (!endpoint) {
      const registration = await ensureVoiceServiceWorker();
      const subscription = await registration.pushManager.getSubscription();
      endpoint = subscription && typeof subscription.endpoint === "string" ? subscription.endpoint : "";
    }
    notificationEndpointRef.current = endpoint;
    const state = snapshot ?? await api.getNotificationSubscriptionState();
    const current = endpoint ? state.subscriptions.find((item) => item && item.endpoint === endpoint) : null;
    setPushNotificationsEnabled(Boolean(current && current.notifications_enabled));
  };

  useEffect(() => {
    if (!bootstrapLoaded || !notificationsSupported || notificationDeviceClass() !== "mobile") return;
    syncNotificationSubscriptionState().catch(() => undefined);
  }, [bootstrapLoaded, notificationsSupported, voiceSettings.notifications?.vapid_public_key]);

  const prunePlayedReplySoundTextKeys = (nowTs: number) => {
    for (const [key, ts] of playedReplySoundTextKeysRef.current.entries()) {
      if ((nowTs - ts) > REPLY_SOUND_TEXT_DEDUPE_MS) {
        playedReplySoundTextKeysRef.current.delete(key);
      }
    }
  };

  const rememberPlayedReplySound = (sessionId: string, row: Record<string, unknown>) => {
    const messageId = typeof row.message_id === "string" ? row.message_id.trim() : "";
    if (messageId) {
      playedReplySoundKeysRef.current.add(`id:${messageId}`);
    }
    const nowTs = Date.now();
    prunePlayedReplySoundTextKeys(nowTs);
    const textKey = replySoundTextKey(sessionId, row);
    if (textKey) {
      playedReplySoundTextKeysRef.current.set(textKey, nowTs);
    }
  };

  const hasPlayedReplySound = (sessionId: string, row: Record<string, unknown>, key: string) => {
    if (playedReplySoundKeysRef.current.has(key)) {
      return true;
    }
    const nowTs = Date.now();
    prunePlayedReplySoundTextKeys(nowTs);
    const textKey = replySoundTextKey(sessionId, row);
    return textKey ? playedReplySoundTextKeysRef.current.has(textKey) : false;
  };

  const showDesktopNotification = (title: string, body: string, messageId?: string) => {
    if (notificationDeviceClass() !== "desktop" || notificationPermission !== "granted" || typeof Notification === "undefined") {
      return;
    }
    const trimmedBody = body.replace(/\s+/g, " ").trim();
    if (!trimmedBody) return;
    const id = String(messageId || "").trim();
    if (id && deliveredNotificationIdsRef.current.has(id)) return;
    new Notification(title.trim() || "Session", {
      body: trimmedBody.length <= 180 ? trimmedBody : `${trimmedBody.slice(0, 179).trimEnd()}...`,
      tag: id || `desktop:${Date.now()}`,
    });
    if (id) deliveredNotificationIdsRef.current.add(id);
  };

  const refreshNotificationFeed = useCallback(async (prime = false) => {
    if (!bootstrapLoaded || !notificationsSupported) {
      return;
    }
    const desktopNotificationsEnabled = (
      notificationsEnabled
      && notificationPermission === "granted"
      && typeof Notification !== "undefined"
      && notificationDeviceClass() === "desktop"
    );
    if (!pageVisible || (!replySoundEnabled && !desktopNotificationsEnabled)) {
      return;
    }
    let response;
    try {
      response = await api.getNotificationsFeed(notificationFeedCursorRef.current);
    } catch {
      return;
    }
    let maxSeen = notificationFeedCursorRef.current;
    for (const item of response.items || []) {
      const updatedTs = Number(item.updated_ts || 0);
      if (updatedTs > maxSeen) maxSeen = updatedTs;
      if (prime) continue;
      const messageId = typeof item.message_id === "string" ? item.message_id.trim() : "";
      const replySoundKey = messageId ? `id:${messageId}` : "";
      const replySoundSessionId = typeof (item as any).session_id === "string"
        ? String((item as any).session_id)
        : String(item.session_display_name || "");
      const replySoundRow = {
        message_id: messageId,
        notification_text: item.notification_text,
      } satisfies Record<string, unknown>;
      if (replySoundEnabled && replySoundKey && !hasPlayedReplySound(replySoundSessionId, replySoundRow, replySoundKey)) {
        playReplyBeepRef.current();
        rememberPlayedReplySound(replySoundSessionId, replySoundRow);
      }
      if (desktopNotificationsEnabled) {
        showDesktopNotification(
          String(item.session_display_name || "Session"),
          String(item.notification_text || ""),
          item.message_id,
        );
      }
    }
    notificationFeedCursorRef.current = maxSeen;
  }, [bootstrapLoaded, notificationPermission, notificationsEnabled, notificationsSupported, pageVisible, replySoundEnabled]);

  useEffect(() => {
    if (!bootstrapLoaded || !notificationsSupported) {
      return undefined;
    }
    notificationFeedCursorRef.current = Date.now() / 1000;
    void refreshNotificationFeed(true);
    const intervalId = window.setInterval(() => {
      void refreshNotificationFeed(false);
    }, realtimeConnected ? REALTIME_NOTIFICATION_RECOVERY_MS : NOTIFICATION_FEED_POLL_MS);
    return () => {
      window.clearInterval(intervalId);
    };
  }, [bootstrapLoaded, notificationsSupported, realtimeConnected, refreshNotificationFeed]);

  useEffect(() => {
    const nextSeen = new Set<string>();
    for (const signature of finalResponseSignatures) {
      const key = String(signature.key || "").trim();
      const sessionId = String(signature.sessionId || "").trim();
      if (!key || !sessionId) {
        continue;
      }
      nextSeen.add(key);
      const suppressReplySound = suppressedReplySoundSessionIdsRef.current.has(sessionId);
      const [, messageId] = key.split("\u0001");
      const replySoundKey = messageId ? `id:${messageId.trim()}` : key;
      const replySoundRow = {
        message_id: messageId,
        notification_text: signature.notificationText,
      } satisfies Record<string, unknown>;
      if (
        !suppressReplySound
        && finalResponseBeepPrimedRef.current
        && replySoundEnabled
        && !seenFinalResponseKeysRef.current.has(key)
        && !hasPlayedReplySound(sessionId, replySoundRow, replySoundKey)
      ) {
        playReplyBeepRef.current();
        rememberPlayedReplySound(sessionId, replySoundRow);
      }
    }
    seenFinalResponseKeysRef.current = nextSeen;
    finalResponseBeepPrimedRef.current = true;
  }, [finalResponseSignatures, replySoundEnabled, suppressedReplySoundSessionIdsRef]);

  useEffect(() => {
    if (
      !bootstrapLoaded
      || !notificationsSupported
      || notificationDeviceClass() !== "desktop"
      || !notificationsEnabled
      || notificationPermission !== "granted"
      || !activeSessionId
    ) {
      return;
    }

    const activeSignature = finalResponseSignatures.find((signature) => signature.sessionId === activeSessionId);
    if (!activeSignature) {
      return;
    }
    const [, messageId, fallbackId] = activeSignature.key.split("\u0001");
    const notificationText = activeSignature.notificationText;
    const desktopTag = messageId || fallbackId;
    if (notificationText) {
      showDesktopNotification(activeTitle, notificationText, desktopTag);
      return;
    }
    if (!messageId) {
      return;
    }
    const lookupState = notificationLookupStateRef.current.get(messageId);
    if (lookupState?.terminal || (lookupState && lookupState.retryAfter > Date.now()) || resolvingNotificationIdsRef.current.has(messageId)) {
      return;
    }
    resolvingNotificationIdsRef.current.add(messageId);
    api.getNotificationMessage(messageId)
      .then((response) => {
        const text = String(response.notification_text || "").trim();
        const status = String(response.summary_status || "").trim();
        if (text && (!status || FINAL_NOTIFICATION_SUMMARY_STATUSES.has(status))) {
          notificationLookupStateRef.current.set(messageId, {
            retryAfter: Number.POSITIVE_INFINITY,
            terminal: true,
          });
          showDesktopNotification(activeTitle, text, messageId);
          return;
        }

        notificationLookupStateRef.current.set(messageId, {
          retryAfter: status && FINAL_NOTIFICATION_SUMMARY_STATUSES.has(status)
            ? Number.POSITIVE_INFINITY
            : Date.now() + NOTIFICATION_MESSAGE_RETRY_MS,
          terminal: Boolean(status && FINAL_NOTIFICATION_SUMMARY_STATUSES.has(status)),
        });
      })
      .catch(() => undefined)
      .finally(() => {
        resolvingNotificationIdsRef.current.delete(messageId);
      });
  }, [activeSessionId, activeTitle, bootstrapLoaded, finalResponseSignatures, notificationPermission, notificationsEnabled, notificationsSupported]);

  const notificationLabel = !bootstrapLoaded
    ? "Notifications loading"
    : !notificationsSupported
      ? "Notifications unavailable"
      : notificationsEnabled
        ? notificationDeviceClass() === "mobile"
          ? pushNotificationsEnabled
            ? "Notifications on (push)"
            : "Notifications pending"
          : notificationPermission === "granted" || notificationPermission === "unsupported"
            ? "Notifications on"
            : "Notifications pending"
        : "Notifications off";

  const toggleNotifications = async () => {
    if (!bootstrapLoaded || !notificationsSupported) {
      setNotificationsEnabled(false);
      return;
    }
    const next = !notificationsEnabled;
    if (!next) {
      if (notificationDeviceClass() === "mobile" && notificationEndpointRef.current) {
        const snapshot = await api.toggleNotificationSubscription(notificationEndpointRef.current, false);
        await syncNotificationSubscriptionState(snapshot);
      }
      setNotificationsEnabled(false);
      return;
    }
    if (typeof Notification !== "undefined" && Notification.permission !== "granted") {
      const permission = await Notification.requestPermission();
      setNotificationPermission(permission);
      if (permission !== "granted") {
        setNotificationsEnabled(false);
        return;
      }
    }
    if (typeof Notification !== "undefined" && Notification.permission === "granted") {
      setNotificationPermission("granted");
    }
    if (notificationDeviceClass() === "mobile") {
      if (typeof PushManager === "undefined") {
        setNotificationsEnabled(false);
        return;
      }
      const registration = await ensureVoiceServiceWorker();
      const subscriptionState = await api.getNotificationSubscriptionState();
      let subscription = await registration.pushManager.getSubscription();
      const subscriptionEndpoint = typeof subscription?.endpoint === "string" ? subscription.endpoint : "";
      const currentSubscription = subscriptionEndpoint
        ? subscriptionState.subscriptions.find((item) => item && item.endpoint === subscriptionEndpoint)
        : null;
      if (isStalePushSubscriptionEndpoint(subscription?.endpoint) || (subscription && !currentSubscription)) {
        try {
          await subscription?.unsubscribe?.();
        } catch {
          // Ignore stale subscription cleanup failures and continue with a fresh subscribe.
        }
        subscription = null;
      }
      const publicKey = String(voiceSettings.notifications?.vapid_public_key || "").trim();
      if (!subscription) {
        if (!publicKey) {
          setNotificationsEnabled(false);
          return;
        }
        subscription = await registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: base64UrlToUint8Array(publicKey),
        });
      }
      const snapshot = await api.upsertNotificationSubscription({
        subscription: subscription.toJSON(),
        user_agent: navigator.userAgent,
        device_label: "current-device",
        device_class: notificationDeviceClass(),
      });
      const endpoint = typeof subscription.endpoint === "string" ? subscription.endpoint : "";
      await syncNotificationSubscriptionState(snapshot, endpoint);
    }
    setNotificationsEnabled(true);
  };

  return {
    notificationLabel,
    notificationsEnabled,
    pushNotificationsEnabled,
    refreshNotificationFeed: () => refreshNotificationFeed(false),
    replySoundEnabled,
    showRealtimeNotification: (payload: Record<string, unknown>) => {
      if (!bootstrapLoaded || !notificationsSupported || !notificationsEnabled || notificationDeviceClass() !== "desktop" || notificationPermission !== "granted") {
        return;
      }
      showDesktopNotification(
        String(payload.title || payload.session_display_name || "Session"),
        String(payload.body || payload.notification_text || ""),
        typeof payload.message_id === "string" ? payload.message_id : undefined,
      );
    },
    setReplySoundEnabled,
    toggleNotifications,
  };
}
