import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Globe, Zap, MessageSquareText, Key, KeyRound, Loader2 } from "lucide-react";
import type { ConnectionFormState, FormUpdater } from "./types";

export interface SecretPresence {
  evolutionApiKey: boolean;
  evolutionInstanceToken: boolean;
  chatwootApiToken: boolean;
}

interface Props {
  form: ConnectionFormState;
  updateForm: FormUpdater;
  isEditing: boolean;
  secrets: SecretPresence;
  onResolveToken: () => void;
  isResolvingToken: boolean;
}

/**
 * Secrets are write-only (ADR-0006 Am. 4): the API never returns them, so the
 * inputs start empty. A blank field means "keep the stored value"; typing a
 * value replaces it on save. Clearing is not supported.
 */
function SecretStatus({ present }: { present: boolean }) {
  return (
    <Badge
      variant="outline"
      className={`text-[10px] py-0 h-5 gap-1 ${present ? "border-emerald-500/30 text-emerald-400" : "border-muted-foreground/30 text-muted-foreground"}`}
    >
      <KeyRound className="h-2.5 w-2.5" />
      {present ? "definida" : "não definida"}
    </Badge>
  );
}

export function CredentialsTab({
  form, updateForm, isEditing, secrets, onResolveToken, isResolvingToken,
}: Props) {
  const keepHint = (present: boolean) =>
    present ? "Deixe em branco para manter a atual." : undefined;

  return (
    <div className="space-y-6">
      {/* Geral */}
      <Card className="border-border/50">
        <CardHeader className="pb-4">
          <div className="flex items-center gap-2">
            <Globe className="h-4 w-4 text-primary" />
            <CardTitle className="text-base">Geral</CardTitle>
          </div>
          <CardDescription>Nome de identificação da conexão</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="space-y-2">
            <Label htmlFor="name">Nome da Conexão</Label>
            <Input id="name" placeholder="Ex: WhatsApp Principal" value={form.name} onChange={(e) => updateForm({ name: e.target.value })} required />
          </div>
        </CardContent>
      </Card>

      {/* Evolution GO */}
      <Card className="border-border/50">
        <CardHeader className="pb-4">
          <div className="flex items-center gap-2">
            <Zap className="h-4 w-4 text-emerald-500" />
            <CardTitle className="text-base">Evolution GO</CardTitle>
          </div>
          <CardDescription>Credenciais da API Evolution GO</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="evoUrl">URL da API</Label>
            <Input id="evoUrl" placeholder="https://evogo.seudominio.com.br" value={form.evolutionApiUrl} onChange={(e) => updateForm({ evolutionApiUrl: e.target.value })} required />
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label htmlFor="evoKey">GLOBAL_API_KEY (Admin)</Label>
              {isEditing && <SecretStatus present={secrets.evolutionApiKey} />}
            </div>
            <Input
              id="evoKey"
              type="password"
              placeholder={isEditing ? keepHint(secrets.evolutionApiKey) ?? "Sua GLOBAL_API_KEY" : "Sua GLOBAL_API_KEY"}
              value={form.evolutionApiKey}
              onChange={(e) => updateForm({ evolutionApiKey: e.target.value })}
              required={!isEditing}
              autoComplete="new-password"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="evoInstance">Nome da Instância</Label>
            <Input id="evoInstance" placeholder="Ex: minha-instancia" value={form.evolutionInstanceName} onChange={(e) => updateForm({ evolutionInstanceName: e.target.value })} required />
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label htmlFor="evoToken">Token da Instância (opcional)</Label>
              {isEditing && <SecretStatus present={secrets.evolutionInstanceToken} />}
            </div>
            <div className="flex gap-2">
              <Input
                id="evoToken"
                type="password"
                placeholder={isEditing ? keepHint(secrets.evolutionInstanceToken) ?? "Preenchido automaticamente" : "Preenchido automaticamente"}
                value={form.evolutionInstanceToken}
                onChange={(e) => updateForm({ evolutionInstanceToken: e.target.value })}
                className="flex-1"
                autoComplete="new-password"
              />
              {isEditing && (
                <Button type="button" variant="outline" size="icon" onClick={onResolveToken} disabled={isResolvingToken} title="Resolver token automaticamente">
                  {isResolvingToken ? <Loader2 className="h-4 w-4 animate-spin" /> : <Key className="h-4 w-4" />}
                </Button>
              )}
            </div>
            <p className="text-xs text-muted-foreground">Será resolvido automaticamente a partir da GLOBAL_API_KEY se não informado. O token resolvido fica apenas no servidor.</p>
          </div>
        </CardContent>
      </Card>

      {/* Chatwoot */}
      <Card className="border-border/50">
        <CardHeader className="pb-4">
          <div className="flex items-center gap-2">
            <MessageSquareText className="h-4 w-4 text-blue-500" />
            <CardTitle className="text-base">Chatwoot</CardTitle>
          </div>
          <CardDescription>Credenciais da API do Chatwoot</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="cwUrl">URL do Chatwoot</Label>
            <Input id="cwUrl" placeholder="https://chat.seudominio.com.br" value={form.chatwootApiUrl} onChange={(e) => updateForm({ chatwootApiUrl: e.target.value })} required />
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label htmlFor="cwToken">API Token</Label>
              {isEditing && <SecretStatus present={secrets.chatwootApiToken} />}
            </div>
            <Input
              id="cwToken"
              type="password"
              placeholder={isEditing ? keepHint(secrets.chatwootApiToken) ?? "User Access Token do Chatwoot" : "User Access Token do Chatwoot"}
              value={form.chatwootApiToken}
              onChange={(e) => updateForm({ chatwootApiToken: e.target.value })}
              required={!isEditing}
              autoComplete="new-password"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="cwAccount">Account ID</Label>
            <Input id="cwAccount" type="number" min={1} value={form.chatwootAccountId} onChange={(e) => updateForm({ chatwootAccountId: parseInt(e.target.value) || 1 })} required />
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
