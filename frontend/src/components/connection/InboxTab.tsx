import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Inbox, Plus, CheckCircle2, Settings, RefreshCw, Loader2,
  MessageCircle, UserCheck, Clock, Shield, MessageSquareText,
  Users, Globe,
} from "lucide-react";
import type { ConnectionFormState, FormUpdater } from "./types";

interface InboxItem {
  id: number;
  name: string;
  channelType?: string;
}

interface Props {
  form: ConnectionFormState;
  updateForm: FormUpdater;
  isEditing: boolean;
  connectionId: number | null;
  inboxes: InboxItem[] | undefined;
  loadingInboxes: boolean;
  chatwootReady: boolean;
  onRefetchInboxes: () => void;
  onSelectInbox: (inboxIdStr: string) => void;
  onCreateInbox: () => void;
  onSyncInboxSettings: () => void;
  isSyncingInbox: boolean;
}

export function InboxTab({
  form, updateForm, isEditing, connectionId,
  inboxes, loadingInboxes, chatwootReady,
  onRefetchInboxes, onSelectInbox, onCreateInbox,
  onSyncInboxSettings, isSyncingInbox,
}: Props) {
  return (
    <div className="space-y-6">
      {/* Inbox Selection */}
      <Card className="border-border/50">
        <CardHeader className="pb-4">
          <div className="flex items-center gap-2">
            <Inbox className="h-4 w-4 text-violet-500" />
            <CardTitle className="text-base">Caixa de Entrada (Inbox)</CardTitle>
          </div>
          <CardDescription>
            Selecione uma inbox existente ou crie uma nova inbox do tipo API dedicada para esta conexão.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {form.chatwootInboxId && (
            <div className="flex items-center gap-2 p-3 rounded-lg bg-emerald-500/10 border border-emerald-500/20">
              <CheckCircle2 className="h-4 w-4 text-emerald-500 shrink-0" />
              <div className="text-sm">
                <span className="text-emerald-400 font-medium">Inbox associada:</span>{" "}
                <span className="text-foreground">{form.chatwootInboxName || `ID #${form.chatwootInboxId}`}</span>
              </div>
            </div>
          )}

          <div className="space-y-2">
            <Label>Selecionar Inbox Existente</Label>
            <div className="flex gap-2">
              <Select value={form.chatwootInboxId ? String(form.chatwootInboxId) : "none"} onValueChange={onSelectInbox} disabled={!chatwootReady}>
                <SelectTrigger className="flex-1">
                  <SelectValue placeholder={!chatwootReady ? "Preencha as credenciais do Chatwoot primeiro" : loadingInboxes ? "Carregando..." : "Selecione uma inbox"} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">Nenhuma (não associar)</SelectItem>
                  {inboxes?.map((inbox) => (
                    <SelectItem key={inbox.id} value={String(inbox.id)}>
                      <div className="flex items-center gap-2">
                        <span>{inbox.name}</span>
                        <span className="text-xs text-muted-foreground">({inbox.channelType?.replace("Channel::", "") || "?"})</span>
                      </div>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button type="button" variant="outline" size="icon" onClick={onRefetchInboxes} disabled={!chatwootReady || loadingInboxes} title="Recarregar">
                {loadingInboxes ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
              </Button>
            </div>
            {chatwootReady && inboxes && (
              <p className="text-xs text-muted-foreground">{inboxes.length} inbox(es) encontrada(s)</p>
            )}
          </div>

          <Separator />
          <Button type="button" variant="outline" onClick={onCreateInbox} className="gap-2 w-full">
            <Plus className="h-4 w-4" />
            Criar Nova Caixa de Entrada (API)
          </Button>
        </CardContent>
      </Card>

      {/* Inbox Settings */}
      <Card className="border-border/50">
        <CardHeader className="pb-4">
          <div className="flex items-center gap-2">
            <Settings className="h-4 w-4 text-orange-500" />
            <CardTitle className="text-base">Configurações da Inbox</CardTitle>
          </div>
          <CardDescription>
            Estas configurações são sincronizadas com o Chatwoot. Altere aqui e clique em "Sincronizar" para aplicar.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          {/* Greeting */}
          <SettingToggle
            icon={<MessageCircle className="h-4 w-4 text-blue-400" />}
            label="Mensagem de Saudação"
            description="Envia uma mensagem automática quando um novo contato inicia uma conversa."
            checked={form.inboxGreetingEnabled}
            onChange={(v) => updateForm({ inboxGreetingEnabled: v })}
          >
            {form.inboxGreetingEnabled && (
              <Textarea placeholder="Olá! Seja bem-vindo. Como posso ajudar?" value={form.inboxGreetingMessage} onChange={(e) => updateForm({ inboxGreetingMessage: e.target.value })} rows={3} />
            )}
          </SettingToggle>

          <Separator />

          <SettingToggle
            icon={<UserCheck className="h-4 w-4 text-green-400" />}
            label="Auto-Atribuição de Agentes"
            description="Atribui automaticamente conversas novas aos agentes disponíveis."
            checked={form.inboxAutoAssignment}
            onChange={(v) => updateForm({ inboxAutoAssignment: v })}
          />

          <Separator />

          <SettingToggle
            icon={<CheckCircle2 className="h-4 w-4 text-yellow-400" />}
            label="Pesquisa de Satisfação (CSAT)"
            description="Envia uma pesquisa de satisfação quando a conversa é resolvida."
            checked={form.inboxCsatEnabled}
            onChange={(v) => updateForm({ inboxCsatEnabled: v })}
          />

          <Separator />

          {/* Working Hours */}
          <SettingToggle
            icon={<Clock className="h-4 w-4 text-purple-400" />}
            label="Horário de Funcionamento"
            description="Ativa mensagem automática fora do horário de expediente."
            checked={form.inboxWorkingHoursEnabled}
            onChange={(v) => updateForm({ inboxWorkingHoursEnabled: v })}
          >
            {form.inboxWorkingHoursEnabled && (
              <div className="space-y-3 pl-6">
                <div className="space-y-2">
                  <Label>Mensagem Fora do Expediente</Label>
                  <Textarea placeholder="Nosso horário de atendimento é de segunda a sexta, das 8h às 18h." value={form.inboxOutOfOfficeMessage} onChange={(e) => updateForm({ inboxOutOfOfficeMessage: e.target.value })} rows={3} />
                </div>
                <div className="space-y-2">
                  <Label>Fuso Horário</Label>
                  <TimezoneSelect value={form.inboxTimezone} onChange={(v) => updateForm({ inboxTimezone: v })} />
                </div>
              </div>
            )}
          </SettingToggle>

          <Separator />

          <SettingToggle
            icon={<MessageSquareText className="h-4 w-4 text-cyan-400" />}
            label="Permitir Mensagens Após Resolução"
            description="Permite que o contato envie mensagens após a conversa ser resolvida (reabre a conversa)."
            checked={form.inboxAllowAfterResolved}
            onChange={(v) => updateForm({ inboxAllowAfterResolved: v })}
          />

          <Separator />

          <SettingToggle
            icon={<Shield className="h-4 w-4 text-red-400" />}
            label="Bloquear em Conversa Única"
            description="Impede que o mesmo contato tenha múltiplas conversas abertas simultaneamente."
            checked={form.inboxLockSingleConversation}
            onChange={(v) => updateForm({ inboxLockSingleConversation: v })}
          />

          <Separator />

          {/* Sender Name Type */}
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <Users className="h-4 w-4 text-indigo-400" />
              <Label className="font-medium">Tipo de Nome do Remetente</Label>
            </div>
            <p className="text-xs text-muted-foreground">Define como o nome do agente aparece nas mensagens.</p>
            <Select value={form.inboxSenderNameType} onValueChange={(v) => updateForm({ inboxSenderNameType: v })}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="friendly">Amigável (primeiro nome)</SelectItem>
                <SelectItem value="professional">Profissional (nome completo)</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <Separator />

          {/* Business Name */}
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <Globe className="h-4 w-4 text-teal-400" />
              <Label className="font-medium">Nome do Negócio</Label>
            </div>
            <p className="text-xs text-muted-foreground">Nome da empresa exibido nas conversas.</p>
            <Input placeholder="Ex: Minha Empresa" value={form.inboxBusinessName} onChange={(e) => updateForm({ inboxBusinessName: e.target.value })} />
          </div>

          {/* Sync button */}
          {isEditing && form.chatwootInboxId && (
            <>
              <Separator />
              <Button type="button" variant="outline" onClick={onSyncInboxSettings} disabled={isSyncingInbox} className="gap-2 w-full">
                {isSyncingInbox ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
                Sincronizar Configurações com o Chatwoot
              </Button>
              <p className="text-xs text-muted-foreground text-center">
                Aplica as configurações acima diretamente na inbox do Chatwoot via API.
              </p>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// ─── Reusable sub-components ────────────────────────────────────────

function SettingToggle({ icon, label, description, checked, onChange, children }: {
  icon: React.ReactNode;
  label: string;
  description: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  children?: React.ReactNode;
}) {
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            {icon}
            <Label className="font-medium">{label}</Label>
          </div>
          <p className="text-xs text-muted-foreground">{description}</p>
        </div>
        <Switch checked={checked} onCheckedChange={onChange} />
      </div>
      {children}
    </div>
  );
}

function TimezoneSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const timezones = [
    { value: "America/Sao_Paulo", label: "São Paulo (GMT-3)" },
    { value: "America/Manaus", label: "Manaus (GMT-4)" },
    { value: "America/Bahia", label: "Bahia (GMT-3)" },
    { value: "America/Recife", label: "Recife (GMT-3)" },
    { value: "America/Fortaleza", label: "Fortaleza (GMT-3)" },
    { value: "America/Belem", label: "Belém (GMT-3)" },
    { value: "America/Cuiaba", label: "Cuiabá (GMT-4)" },
    { value: "America/Porto_Velho", label: "Porto Velho (GMT-4)" },
    { value: "America/Rio_Branco", label: "Rio Branco (GMT-5)" },
    { value: "America/Noronha", label: "Fernando de Noronha (GMT-2)" },
    { value: "UTC", label: "UTC" },
  ];

  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger><SelectValue /></SelectTrigger>
      <SelectContent>
        {timezones.map((tz) => (
          <SelectItem key={tz.value} value={tz.value}>{tz.label}</SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
