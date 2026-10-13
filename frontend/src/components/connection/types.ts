/** Shared form state type for the connection form */
export interface ConnectionFormState {
  name: string;
  evolutionApiUrl: string;
  evolutionApiKey: string;
  evolutionInstanceName: string;
  evolutionInstanceToken: string;
  chatwootApiUrl: string;
  chatwootApiToken: string;
  chatwootAccountId: number;
  chatwootInboxId: number | undefined;
  chatwootInboxName: string;
  // Bridge behavior
  ignoreGroups: boolean;
  signMessages: boolean;
  autoReadMessages: boolean;
  rejectCalls: boolean;
  rejectCallMessage: string;
  // N8N integration
  n8nWebhookUrl: string;
  // Inbox settings
  inboxGreetingEnabled: boolean;
  inboxGreetingMessage: string;
  inboxAutoAssignment: boolean;
  inboxCsatEnabled: boolean;
  inboxWorkingHoursEnabled: boolean;
  inboxOutOfOfficeMessage: string;
  inboxTimezone: string;
  inboxAllowAfterResolved: boolean;
  inboxLockSingleConversation: boolean;
  inboxSenderNameType: string;
  inboxBusinessName: string;
}

export const DEFAULT_FORM_STATE: ConnectionFormState = {
  name: "",
  evolutionApiUrl: "",
  evolutionApiKey: "",
  evolutionInstanceName: "",
  evolutionInstanceToken: "",
  chatwootApiUrl: "",
  chatwootApiToken: "",
  chatwootAccountId: 1,
  chatwootInboxId: undefined,
  chatwootInboxName: "",
  ignoreGroups: true,
  signMessages: false,
  autoReadMessages: false,
  rejectCalls: false,
  rejectCallMessage: "",
  n8nWebhookUrl: "",
  inboxGreetingEnabled: false,
  inboxGreetingMessage: "",
  inboxAutoAssignment: true,
  inboxCsatEnabled: false,
  inboxWorkingHoursEnabled: false,
  inboxOutOfOfficeMessage: "",
  inboxTimezone: "America/Sao_Paulo",
  inboxAllowAfterResolved: true,
  inboxLockSingleConversation: false,
  inboxSenderNameType: "friendly",
  inboxBusinessName: "",
};

export type FormUpdater = (partial: Partial<ConnectionFormState>) => void;
