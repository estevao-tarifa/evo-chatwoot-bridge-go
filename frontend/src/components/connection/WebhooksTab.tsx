import { api } from "@/api";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { toast } from "sonner";
import { Zap, MessageSquareText, Loader2, CheckCircle2, Eye, Copy } from "lucide-react";
import { useState } from "react";
import type { ConnectionFormState } from "./types";

interface Props {
  form: ConnectionFormState;
  connectionId: number | null;
  bridgeUrl: string;
  bridgeUrlSource?: "database" | "env" | "none";
  onBridgeUrlChange: (url: string) => void;
  onConfigureEvolution: () => void;
  onConfigureChatwoot: () => void;
  onConfigureBoth: () => void;
  isConfiguringEvolution: boolean;
  isConfiguringChatwoot: boolean;
}

type WebhookSystem = "evolution" | "chatwoot";

export function WebhooksTab({
  form, connectionId, bridgeUrl, bridgeUrlSource, onBridgeUrlChange,
  onConfigureEvolution, onConfigureChatwoot, onConfigureBoth,
  isConfiguringEvolution, isConfiguringChatwoot,
}: Props) {
  // The full URL contains a derived token and is only fetched on explicit user
  // action (§6.5 R2). It is held in component state and discarded on close —
  // never persisted.
  const [reveal, setReveal] = useState<{ system: WebhookSystem; url: string } | null>(null);
  const [revealing, setRevealing] = useState<WebhookSystem | null>(null);

  if (!connectionId) return null;

  const sourceLabel = bridgeUrlSource === "env"
    ? "Configurada via variável de ambiente (BRIDGE_URL)"
    : bridgeUrlSource === "database"
    ? "Salva no banco de dados"
    : "Informe a URL pública onde o Bridge está acessível.";

  const handleReveal = async (system: WebhookSystem) => {
    setRevealing(system);
    try {
      const result =
        system === "evolution"
          ? await api.connections.revealEvolutionWebhookUrl(connectionId)
          : await api.connections.revealChatwootWebhookUrl(connectionId);
      setReveal({ system, url: result.webhookUrl });
    } catch (e) {
      toast.error(`Erro ao obter a URL: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setRevealing(null);
    }
  };

  const handleCopy = async () => {
    if (!reveal) return;
    try {
      await navigator.clipboard.writeText(reveal.url);
      toast.success("URL copiada!");
    } catch {
      toast.error("Não foi possível copiar. Copie manualmente.");
    }
  };

  return (
    <Card className="border-border/50">
      <CardHeader className="pb-4">
        <div className="flex items-center gap-2">
          <Zap className="h-4 w-4 text-amber-500" />
          <CardTitle className="text-base">Configurar Webhooks</CardTitle>
        </div>
        <CardDescription>
          Configure os webhooks para que as mensagens fluam nos dois sentidos.
          A URL contém um token derivado e só é exibida quando você pedir.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="space-y-2">
          <Label htmlFor="bridgeUrl">URL pública do Bridge</Label>
          <div className="relative">
            <Input id="bridgeUrl" placeholder="https://bridge.seudominio.com.br" value={bridgeUrl} onChange={(e) => onBridgeUrlChange(e.target.value)} />
            {bridgeUrl && bridgeUrlSource && bridgeUrlSource !== "none" && (
              <CheckCircle2 className="absolute right-3 top-1/2 -translate-y-1/2 h-4 w-4 text-emerald-500" />
            )}
          </div>
          <p className="text-xs text-muted-foreground">{sourceLabel}</p>
        </div>

        {/* Evolution GO Webhook */}
        <div className="rounded-lg border border-border/50 p-4 space-y-3">
          <div className="flex items-center gap-2">
            <Zap className="h-3.5 w-3.5 text-emerald-500" />
            <span className="text-sm font-medium">Evolution GO → Bridge</span>
            <span className="text-xs text-muted-foreground">(WhatsApp → Chatwoot)</span>
          </div>
          <p className="text-xs text-muted-foreground">
            O webhook da Evolution GO é registrado apontando para a URL do Bridge.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="outline" size="sm" onClick={onConfigureEvolution} disabled={isConfiguringEvolution || !bridgeUrl} className="gap-2">
              {isConfiguringEvolution ? <Loader2 className="h-4 w-4 animate-spin" /> : <Zap className="h-4 w-4" />}
              Configurar na Evolution GO
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => handleReveal("evolution")} disabled={revealing === "evolution"} className="gap-2">
              {revealing === "evolution" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Eye className="h-4 w-4" />}
              Revelar URL
            </Button>
          </div>
        </div>

        {/* Chatwoot Webhook */}
        <div className="rounded-lg border border-border/50 p-4 space-y-3">
          <div className="flex items-center gap-2">
            <MessageSquareText className="h-3.5 w-3.5 text-blue-500" />
            <span className="text-sm font-medium">Chatwoot → Bridge</span>
            <span className="text-xs text-muted-foreground">(Chatwoot → WhatsApp)</span>
          </div>
          {!form.chatwootInboxId && (
            <p className="text-xs text-amber-500">Associe uma Inbox primeiro para configurar o webhook do Chatwoot.</p>
          )}
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="outline" size="sm" onClick={onConfigureChatwoot} disabled={isConfiguringChatwoot || !bridgeUrl || !form.chatwootInboxId} className="gap-2">
              {isConfiguringChatwoot ? <Loader2 className="h-4 w-4 animate-spin" /> : <MessageSquareText className="h-4 w-4" />}
              Configurar na Inbox do Chatwoot
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => handleReveal("chatwoot")} disabled={revealing === "chatwoot" || !form.chatwootInboxId} className="gap-2">
              {revealing === "chatwoot" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Eye className="h-4 w-4" />}
              Revelar URL
            </Button>
          </div>
        </div>

        {/* Configure both */}
        <Button type="button" onClick={onConfigureBoth} disabled={isConfiguringEvolution || isConfiguringChatwoot || !bridgeUrl} className="gap-2 w-full">
          {(isConfiguringEvolution || isConfiguringChatwoot) ? <Loader2 className="h-4 w-4 animate-spin" /> : <Zap className="h-4 w-4" />}
          Configurar Ambos os Webhooks
        </Button>
      </CardContent>

      <Dialog open={!!reveal} onOpenChange={(open) => { if (!open) setReveal(null); }}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {reveal?.system === "chatwoot" ? "URL do webhook Chatwoot" : "URL do webhook Evolution GO"}
            </DialogTitle>
            <DialogDescription>
              Registre esta URL manualmente. Ela contém um token e não é armazenada
              neste navegador.
            </DialogDescription>
          </DialogHeader>
          <div className="flex items-center gap-2">
            <Input readOnly value={reveal?.url ?? ""} onFocus={(e) => e.currentTarget.select()} className="font-mono text-xs" />
            <Button type="button" size="icon" onClick={handleCopy} title="Copiar">
              <Copy className="h-4 w-4" />
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
