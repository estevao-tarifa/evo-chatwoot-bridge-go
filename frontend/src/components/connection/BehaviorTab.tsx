import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Settings, Users, UserCheck, Eye, PhoneOff, Workflow,
} from "lucide-react";
import type { ConnectionFormState, FormUpdater } from "./types";

interface Props {
  form: ConnectionFormState;
  updateForm: FormUpdater;
}

export function BehaviorTab({ form, updateForm }: Props) {
  return (
    <Card className="border-border/50">
      <CardHeader className="pb-4">
        <div className="flex items-center gap-2">
          <Settings className="h-4 w-4 text-amber-500" />
          <CardTitle className="text-base">Comportamento do Bridge</CardTitle>
        </div>
        <CardDescription>
          Configure como o Bridge processa as mensagens entre WhatsApp e Chatwoot.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        {/* Ignore Groups */}
        <ToggleRow
          icon={<Users className="h-4 w-4 text-blue-400" />}
          label="Ignorar Grupos"
          description="Ignora mensagens de grupos do WhatsApp. Apenas conversas individuais são processadas."
          checked={form.ignoreGroups}
          onChange={(v) => updateForm({ ignoreGroups: v })}
        />

        <Separator />

        {/* Sign Messages */}
        <ToggleRow
          icon={<UserCheck className="h-4 w-4 text-violet-400" />}
          label="Mensagens Assinadas"
          description="Adiciona o nome do agente antes da mensagem enviada pelo Chatwoot."
          checked={form.signMessages}
          onChange={(v) => updateForm({ signMessages: v })}
        >
          {form.signMessages && (
            <div className="ml-6 p-3 rounded-lg bg-muted/50 text-sm">
              <p className="text-muted-foreground">Exemplo de mensagem assinada:</p>
              <p className="mt-1 font-mono text-xs">
                <span className="font-bold">*João Silva:*</span><br />
                Olá! Como posso ajudar?
              </p>
            </div>
          )}
        </ToggleRow>

        <Separator />

        {/* Auto Read Messages */}
        <ToggleRow
          icon={<Eye className="h-4 w-4 text-emerald-400" />}
          label="Marcar como Lido"
          description="Marca automaticamente as mensagens recebidas como lidas no WhatsApp (tick azul)."
          checked={form.autoReadMessages}
          onChange={(v) => updateForm({ autoReadMessages: v })}
        />

        <Separator />

        {/* Reject Calls */}
        <ToggleRow
          icon={<PhoneOff className="h-4 w-4 text-red-400" />}
          label="Rejeitar Chamadas"
          description="Rejeita chamadas de voz/vídeo recebidas e envia uma mensagem automática."
          checked={form.rejectCalls}
          onChange={(v) => updateForm({ rejectCalls: v })}
        >
          {form.rejectCalls && (
            <div className="pl-6 space-y-2">
              <Label>Mensagem de Rejeição</Label>
              <Textarea
                placeholder="Desculpe, não aceitamos chamadas por este número. Por favor, envie uma mensagem de texto."
                value={form.rejectCallMessage}
                onChange={(e) => updateForm({ rejectCallMessage: e.target.value })}
                rows={2}
              />
            </div>
          )}
        </ToggleRow>

        <Separator />

        {/* N8N Integration */}
        <div className="space-y-3">
          <div className="flex items-center gap-2">
            <Workflow className="h-4 w-4 text-orange-400" />
            <Label className="font-medium">Integração n8n</Label>
          </div>
          <p className="text-xs text-muted-foreground">
            Encaminha todos os eventos (mensagens recebidas e enviadas) para um webhook do n8n para automações.
          </p>
          <div className="space-y-2">
            <Label htmlFor="n8nUrl">URL do Webhook n8n</Label>
            <Input
              id="n8nUrl"
              placeholder="https://n8n.seudominio.com.br/webhook/xxx-xxx"
              value={form.n8nWebhookUrl}
              onChange={(e) => updateForm({ n8nWebhookUrl: e.target.value })}
            />
            <p className="text-xs text-muted-foreground">
              Deixe em branco para desativar. O Bridge enviará um POST com os dados de cada evento.
            </p>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

// ─── Reusable toggle row ────────────────────────────────────────

function ToggleRow({ icon, label, description, checked, onChange, children }: {
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
