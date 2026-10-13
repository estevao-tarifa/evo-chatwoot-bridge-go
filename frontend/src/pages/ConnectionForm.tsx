import { api } from "@/api";
import type { ConnectionCreateRequest, ConnectionUpdateRequest, InboxesQueryRequest } from "@/api";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ArrowLeft, Save, Loader2, Globe, Inbox, Settings, Zap, Plus } from "lucide-react";
import { useState, useEffect, useMemo, useCallback } from "react";
import { useLocation, useParams } from "wouter";
import { toast } from "sonner";

import {
  CredentialsTab,
  InboxTab,
  BehaviorTab,
  WebhooksTab,
  DEFAULT_FORM_STATE,
} from "@/components/connection";
import type { ConnectionFormState, SecretPresence } from "@/components/connection";

export default function ConnectionForm() {
  const params = useParams<{ id: string }>();
  const isEditing = !!params.id;
  const connectionId = params.id ? parseInt(params.id, 10) : null;
  const [, setLocation] = useLocation();
  const queryClient = useQueryClient();

  // ─── Queries ──────────────────────────────────────────────────
  const { data: existingConn, isLoading: loadingConn } = useQuery({
    queryKey: ["connections", connectionId],
    queryFn: () => api.connections.getById(connectionId as number),
    enabled: isEditing && connectionId !== null,
  });

  const { data: savedBridgeUrl } = useQuery({
    queryKey: ["settings", "bridge-url"],
    queryFn: () => api.settings.getBridgeUrl(),
  });

  const invalidateConnection = () => {
    queryClient.invalidateQueries({ queryKey: ["connections"] });
  };

  // ─── Mutations ────────────────────────────────────────────────
  const createMutation = useMutation({
    mutationFn: (body: ConnectionCreateRequest) => api.connections.create(body),
    onSuccess: () => { toast.success("Conexão criada!"); invalidateConnection(); setLocation("/connections"); },
    onError: (e: Error) => toast.error(`Erro: ${e.message}`),
  });
  const updateMutation = useMutation({
    mutationFn: (body: ConnectionUpdateRequest) =>
      api.connections.update(connectionId as number, body),
    onSuccess: () => {
      toast.success("Conexão atualizada!");
      invalidateConnection();
      queryClient.invalidateQueries({ queryKey: ["connections", connectionId] });
    },
    onError: (e: Error) => toast.error(`Erro ao salvar: ${e.message}`),
  });
  const webhookMutation = useMutation({
    mutationFn: (bridgeUrl: string) =>
      api.connections.configureWebhook(connectionId as number, bridgeUrl),
    onSuccess: () => toast.success("Webhook Evolution configurado!"),
    onError: (e: Error) => toast.error(`Erro no webhook: ${e.message}`),
  });
  const resolveTokenMutation = useMutation({
    mutationFn: () => api.connections.resolveToken(connectionId as number),
    onSuccess: () => {
      toast.success("Token da instância resolvido no servidor!");
      queryClient.invalidateQueries({ queryKey: ["connections", connectionId] });
    },
    onError: (e: Error) => toast.error(`Erro ao resolver token: ${e.message}`),
  });
  const chatwootWebhookMutation = useMutation({
    mutationFn: (bridgeUrl: string) =>
      api.chatwoot.configureWebhook(connectionId as number, bridgeUrl),
    onSuccess: () => toast.success("Webhook Chatwoot configurado!"),
    onError: (e: Error) => toast.error(`Erro no webhook Chatwoot: ${e.message}`),
  });
  const createInboxMutation = useMutation({
    mutationFn: (body: Parameters<typeof api.chatwoot.createInbox>[0]) =>
      api.chatwoot.createInbox(body),
    onSuccess: (data) => {
      toast.success(`Inbox "${data.name}" criada!`);
      updateForm({ chatwootInboxId: data.id, chatwootInboxName: data.name });
      setShowCreateInbox(false);
      setNewInboxName("");
      if (connectionId) queryClient.invalidateQueries({ queryKey: ["connections", connectionId] });
      refetchInboxes();
    },
    onError: (e: Error) => toast.error(`Erro ao criar inbox: ${e.message}`),
  });
  const updateInboxMutation = useMutation({
    mutationFn: (body: Parameters<typeof api.chatwoot.updateInboxSettings>[1]) =>
      api.chatwoot.updateInboxSettings(connectionId as number, body),
    onSuccess: () => {
      toast.success("Configurações da inbox atualizadas no Chatwoot!");
      if (connectionId) queryClient.invalidateQueries({ queryKey: ["connections", connectionId] });
    },
    onError: (e: Error) => toast.error(`Erro ao atualizar inbox: ${e.message}`),
  });

  // ─── Form State ───────────────────────────────────────────────
  const [form, setForm] = useState<ConnectionFormState>(DEFAULT_FORM_STATE);
  const [bridgeUrl, setBridgeUrl] = useState("");
  const [showCreateInbox, setShowCreateInbox] = useState(false);
  const [newInboxName, setNewInboxName] = useState("");
  const [activeTab, setActiveTab] = useState("connection");

  const updateForm = useCallback((partial: Partial<ConnectionFormState>) => {
    setForm((prev) => ({ ...prev, ...partial }));
  }, []);

  // ─── Chatwoot Inboxes Query ───────────────────────────────────
  // Editing: reuse the stored credentials server-side (secrets are write-only).
  // Creating: use the credentials typed in the form (POST body, never a query).
  const chatwootQueryInput = useMemo<InboxesQueryRequest | null>(() => {
    if (isEditing && connectionId) return { connectionId };
    if (!form.chatwootApiUrl || !form.chatwootApiToken || !form.chatwootAccountId) return null;
    try { new URL(form.chatwootApiUrl); } catch { return null; }
    return {
      chatwootApiUrl: form.chatwootApiUrl,
      chatwootApiToken: form.chatwootApiToken,
      chatwootAccountId: form.chatwootAccountId,
    };
  }, [isEditing, connectionId, form.chatwootApiUrl, form.chatwootApiToken, form.chatwootAccountId]);

  const {
    data: inboxes,
    isLoading: loadingInboxes,
    refetch: refetchInboxes,
  } = useQuery({
    queryKey: ["chatwoot", "inboxes", chatwootQueryInput],
    queryFn: () => api.chatwoot.listInboxes(chatwootQueryInput as InboxesQueryRequest),
    enabled: !!chatwootQueryInput,
    retry: false,
  });

  // ─── Load existing connection ─────────────────────────────────
  useEffect(() => {
    if (!existingConn) return;
    const c = existingConn;
    setForm({
      name: c.name ?? "",
      evolutionApiUrl: c.evolutionApiUrl ?? "",
      // Secrets are never returned by the API — always start empty.
      evolutionApiKey: "",
      evolutionInstanceName: c.evolutionInstanceName ?? "",
      evolutionInstanceToken: "",
      chatwootApiUrl: c.chatwootApiUrl ?? "",
      chatwootApiToken: "",
      chatwootAccountId: c.chatwootAccountId ?? 1,
      chatwootInboxId: c.chatwootInboxId || undefined,
      chatwootInboxName: c.chatwootInboxName,
      ignoreGroups: c.ignoreGroups ?? true,
      signMessages: c.signMessages ?? false,
      autoReadMessages: c.autoReadMessages ?? false,
      rejectCalls: c.rejectCalls ?? false,
      rejectCallMessage: c.rejectCallMessage ?? "",
      n8nWebhookUrl: c.n8nWebhookUrl ?? "",
      inboxGreetingEnabled: c.inboxGreetingEnabled ?? false,
      inboxGreetingMessage: c.inboxGreetingMessage ?? "",
      inboxAutoAssignment: c.inboxAutoAssignment ?? true,
      inboxCsatEnabled: c.inboxCsatEnabled ?? false,
      inboxWorkingHoursEnabled: c.inboxWorkingHoursEnabled ?? false,
      inboxOutOfOfficeMessage: c.inboxOutOfOfficeMessage ?? "",
      inboxTimezone: c.inboxTimezone ?? "America/Sao_Paulo",
      inboxAllowAfterResolved: c.inboxAllowAfterResolved ?? true,
      inboxLockSingleConversation: c.inboxLockSingleConversation ?? false,
      inboxSenderNameType: c.inboxSenderNameType ?? "friendly",
      inboxBusinessName: c.inboxBusinessName ?? "",
    });
  }, [existingConn]);

  const secrets: SecretPresence = {
    evolutionApiKey: existingConn?.hasEvolutionApiKey ?? false,
    evolutionInstanceToken: existingConn?.hasEvolutionInstanceToken ?? false,
    chatwootApiToken: existingConn?.hasChatwootApiToken ?? false,
  };

  // Initialize bridgeUrl from saved setting
  useEffect(() => {
    if (savedBridgeUrl?.url && !bridgeUrl) {
      setBridgeUrl(savedBridgeUrl.url);
    }
  }, [savedBridgeUrl, bridgeUrl]);

  // ─── Handlers ─────────────────────────────────────────────────
  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();

    const base = {
      name: form.name,
      evolutionApiUrl: form.evolutionApiUrl,
      evolutionInstanceName: form.evolutionInstanceName,
      chatwootApiUrl: form.chatwootApiUrl,
      chatwootAccountId: form.chatwootAccountId,
      ignoreGroups: form.ignoreGroups,
      signMessages: form.signMessages,
      autoReadMessages: form.autoReadMessages,
      rejectCalls: form.rejectCalls,
      inboxGreetingEnabled: form.inboxGreetingEnabled,
      inboxAutoAssignment: form.inboxAutoAssignment,
      inboxCsatEnabled: form.inboxCsatEnabled,
      inboxWorkingHoursEnabled: form.inboxWorkingHoursEnabled,
      inboxAllowAfterResolved: form.inboxAllowAfterResolved,
      inboxLockSingleConversation: form.inboxLockSingleConversation,
    };

    // Secrets are write-only: send only when a new value was typed.
    const secretPatch: Partial<ConnectionCreateRequest> = {};
    if (form.evolutionApiKey.trim()) secretPatch.evolutionApiKey = form.evolutionApiKey.trim();
    if (form.evolutionInstanceToken.trim()) secretPatch.evolutionInstanceToken = form.evolutionInstanceToken.trim();
    if (form.chatwootApiToken.trim()) secretPatch.chatwootApiToken = form.chatwootApiToken.trim();

    if (isEditing && connectionId) {
      updateMutation.mutate({
        ...base,
        ...secretPatch,
        chatwootInboxId: form.chatwootInboxId ?? null,
        chatwootInboxName: form.chatwootInboxName || null,
        rejectCallMessage: form.rejectCallMessage || null,
        inboxGreetingMessage: form.inboxGreetingMessage || null,
        inboxOutOfOfficeMessage: form.inboxOutOfOfficeMessage || null,
        inboxTimezone: form.inboxTimezone || null,
        inboxSenderNameType: form.inboxSenderNameType || null,
        inboxBusinessName: form.inboxBusinessName || null,
        n8nWebhookUrl: form.n8nWebhookUrl.trim() || null,
      });
    } else {
      createMutation.mutate({
        ...base,
        ...secretPatch,
        evolutionApiKey: form.evolutionApiKey,
        chatwootApiToken: form.chatwootApiToken,
        chatwootInboxId: form.chatwootInboxId || undefined,
        n8nWebhookUrl: form.n8nWebhookUrl.trim() || undefined,
      });
    }
  };

  const handleConfigureWebhook = () => {
    if (!connectionId || !bridgeUrl) { toast.error("Informe a URL pública do Bridge"); return; }
    webhookMutation.mutate(bridgeUrl);
  };

  const handleConfigureChatwootWebhook = () => {
    if (!connectionId || !bridgeUrl) { toast.error("Informe a URL pública do Bridge"); return; }
    if (!form.chatwootInboxId) { toast.error("Associe uma Inbox primeiro"); return; }
    chatwootWebhookMutation.mutate(bridgeUrl);
  };

  const handleConfigureBothWebhooks = () => {
    handleConfigureWebhook();
    if (form.chatwootInboxId) handleConfigureChatwootWebhook();
  };

  const handleSyncInboxSettings = () => {
    if (!connectionId) return;
    updateInboxMutation.mutate({
      greetingEnabled: form.inboxGreetingEnabled,
      greetingMessage: form.inboxGreetingMessage || null,
      autoAssignment: form.inboxAutoAssignment,
      csatEnabled: form.inboxCsatEnabled,
      workingHoursEnabled: form.inboxWorkingHoursEnabled,
      outOfOfficeMessage: form.inboxOutOfOfficeMessage || null,
      timezone: form.inboxTimezone || undefined,
      allowAfterResolved: form.inboxAllowAfterResolved,
      lockSingleConversation: form.inboxLockSingleConversation,
      senderNameType: form.inboxSenderNameType as "friendly" | "professional",
      businessName: form.inboxBusinessName || null,
    });
  };

  const handleSelectInbox = (inboxIdStr: string) => {
    if (inboxIdStr === "none") {
      updateForm({ chatwootInboxId: undefined, chatwootInboxName: "" });
      return;
    }
    const inboxId = parseInt(inboxIdStr, 10);
    const inbox = inboxes?.find((i) => i.id === inboxId);
    updateForm({ chatwootInboxId: inboxId, chatwootInboxName: inbox?.name || "" });
  };

  const handleCreateInbox = () => {
    if (!newInboxName.trim()) { toast.error("Informe o nome da Caixa de Entrada"); return; }
    createInboxMutation.mutate({
      connectionId: connectionId ?? undefined,
      chatwootApiUrl: form.chatwootApiUrl,
      chatwootApiToken: form.chatwootApiToken || undefined,
      chatwootAccountId: form.chatwootAccountId,
      inboxName: newInboxName.trim(),
      greetingEnabled: form.inboxGreetingEnabled,
      greetingMessage: form.inboxGreetingMessage || undefined,
      autoAssignment: form.inboxAutoAssignment,
      timezone: form.inboxTimezone || undefined,
    });
  };

  const isSaving = createMutation.isPending || updateMutation.isPending;

  if (isEditing && loadingConn) {
    return (
      <div className="flex items-center justify-center py-20">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  // ─── Render ───────────────────────────────────────────────────
  return (
    <div className="space-y-6 max-w-3xl">
      <div className="flex items-center gap-3">
        <Button variant="ghost" size="icon" onClick={() => setLocation("/connections")} className="h-8 w-8">
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <div>
          <h1 className="text-2xl font-bold tracking-tight">
            {isEditing ? "Editar Conexão" : "Nova Conexão"}
          </h1>
          <p className="text-muted-foreground text-sm mt-0.5">
            {isEditing ? "Atualize as configurações da conexão" : "Configure uma nova integração Evolution GO ↔ Chatwoot"}
          </p>
        </div>
      </div>

      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList className="grid w-full grid-cols-4">
          <TabsTrigger value="connection" className="gap-1.5"><Globe className="h-3.5 w-3.5" />Conexão</TabsTrigger>
          <TabsTrigger value="inbox" className="gap-1.5"><Inbox className="h-3.5 w-3.5" />Inbox</TabsTrigger>
          <TabsTrigger value="behavior" className="gap-1.5"><Settings className="h-3.5 w-3.5" />Comportamento</TabsTrigger>
          <TabsTrigger value="webhooks" className="gap-1.5" disabled={!isEditing}><Zap className="h-3.5 w-3.5" />Webhooks</TabsTrigger>
        </TabsList>

        <form onSubmit={handleSubmit} className="mt-6">
          <TabsContent value="connection">
            <CredentialsTab
              form={form}
              updateForm={updateForm}
              isEditing={isEditing}
              secrets={secrets}
              onResolveToken={() => connectionId && resolveTokenMutation.mutate()}
              isResolvingToken={resolveTokenMutation.isPending}
            />
          </TabsContent>

          <TabsContent value="inbox">
            <InboxTab
              form={form}
              updateForm={updateForm}
              isEditing={isEditing}
              connectionId={connectionId}
              inboxes={inboxes}
              loadingInboxes={loadingInboxes}
              chatwootReady={!!chatwootQueryInput}
              onRefetchInboxes={() => refetchInboxes()}
              onSelectInbox={handleSelectInbox}
              onCreateInbox={() => setShowCreateInbox(true)}
              onSyncInboxSettings={handleSyncInboxSettings}
              isSyncingInbox={updateInboxMutation.isPending}
            />
          </TabsContent>

          <TabsContent value="behavior">
            <BehaviorTab form={form} updateForm={updateForm} />
          </TabsContent>

          <TabsContent value="webhooks">
            <WebhooksTab
              form={form}
              connectionId={connectionId}
              bridgeUrl={bridgeUrl}
              bridgeUrlSource={savedBridgeUrl?.source}
              onBridgeUrlChange={setBridgeUrl}
              onConfigureEvolution={handleConfigureWebhook}
              onConfigureChatwoot={handleConfigureChatwootWebhook}
              onConfigureBoth={handleConfigureBothWebhooks}
              isConfiguringEvolution={webhookMutation.isPending}
              isConfiguringChatwoot={chatwootWebhookMutation.isPending}
            />
          </TabsContent>

          {/* Submit Button */}
          <div className="flex items-center gap-3 mt-6 pt-6 border-t border-border/50">
            <Button type="submit" disabled={isSaving} className="gap-2">
              {isSaving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
              {isEditing ? "Salvar Alterações" : "Criar Conexão"}
            </Button>
            <Button type="button" variant="outline" onClick={() => setLocation("/connections")}>
              Cancelar
            </Button>
          </div>
        </form>
      </Tabs>

      {/* Create Inbox Dialog */}
      <Dialog open={showCreateInbox} onOpenChange={setShowCreateInbox}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Inbox className="h-5 w-5 text-violet-500" />
              Criar Nova Caixa de Entrada
            </DialogTitle>
            <DialogDescription>
              Uma inbox do tipo API será criada no Chatwoot com as configurações definidas na aba "Inbox".
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="newInboxName">Nome da Inbox</Label>
              <Input id="newInboxName" placeholder="Ex: WhatsApp - Corporativo" value={newInboxName} onChange={(e) => setNewInboxName(e.target.value)} autoFocus />
            </div>
            <div className="rounded-lg bg-muted/50 p-3 text-sm space-y-1">
              <p><span className="text-muted-foreground">Conta Chatwoot:</span> #{form.chatwootAccountId}</p>
              <p><span className="text-muted-foreground">Tipo:</span> API Channel</p>
              <p><span className="text-muted-foreground">Saudação:</span> {form.inboxGreetingEnabled ? "Ativada" : "Desativada"}</p>
              <p><span className="text-muted-foreground">Auto-atribuição:</span> {form.inboxAutoAssignment ? "Ativada" : "Desativada"}</p>
              <p><span className="text-muted-foreground">Fuso horário:</span> {form.inboxTimezone}</p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowCreateInbox(false)}>Cancelar</Button>
            <Button onClick={handleCreateInbox} disabled={createInboxMutation.isPending || !newInboxName.trim()} className="gap-2">
              {createInboxMutation.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
              Criar Inbox
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
