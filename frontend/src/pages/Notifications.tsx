import { useState, useEffect } from "react";
import { api } from "@/api";
import type { NotificationSettingsUpdate } from "@/api";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ErrorState } from "@/components/ErrorState";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { toast } from "sonner";
import {
  Bell, BellOff, MessageSquare, Mail, History,
  CheckCircle2, XCircle, AlertTriangle, Loader2, Save,
  RefreshCw, Settings2, KeyRound,
} from "lucide-react";

export default function Notifications() {
  const queryClient = useQueryClient();
  const settingsQuery = useQuery({
    queryKey: ["notifications", "settings"],
    queryFn: () => api.notifications.getSettings(),
  });
  const connectionsQuery = useQuery({
    queryKey: ["connections"],
    queryFn: () => api.connections.list(),
  });
  const alertsQuery = useQuery({
    queryKey: ["notifications", "alerts"],
    queryFn: () => api.notifications.listAlerts({ limit: 20 }),
  });
  const failureStatesQuery = useQuery({
    queryKey: ["notifications", "failure-states"],
    queryFn: () => api.notifications.failureStates(),
    refetchInterval: 15000,
  });

  const updateMutation = useMutation({
    mutationFn: (body: NotificationSettingsUpdate) =>
      api.notifications.updateSettings(body),
    onSuccess: () => {
      toast.success("Configurações salvas com sucesso!");
      queryClient.invalidateQueries({ queryKey: ["notifications", "settings"] });
    },
    onError: (err: Error) => toast.error(`Erro ao salvar: ${err.message}`),
  });

  // Form state
  const [enabled, setEnabled] = useState(false);
  const [failureThreshold, setFailureThreshold] = useState(3);
  const [cooldownMinutes, setCooldownMinutes] = useState(30);
  const [whatsappEnabled, setWhatsappEnabled] = useState(false);
  const [whatsappConnectionId, setWhatsappConnectionId] = useState<number | null>(null);
  const [whatsappAlertPhone, setWhatsappAlertPhone] = useState("");
  const [emailEnabled, setEmailEnabled] = useState(false);
  const [smtpHost, setSmtpHost] = useState("");
  const [smtpPort, setSmtpPort] = useState(587);
  const [smtpUser, setSmtpUser] = useState("");
  // Write-only: never populated from the API, only sent when the operator types.
  const [smtpPass, setSmtpPass] = useState("");
  const [smtpSecure, setSmtpSecure] = useState(false);
  const [emailFrom, setEmailFrom] = useState("");
  const [emailTo, setEmailTo] = useState("");

  // Load settings into form
  useEffect(() => {
    if (settingsQuery.data) {
      const s = settingsQuery.data;
      setEnabled(s.enabled);
      setFailureThreshold(s.failureThreshold);
      setCooldownMinutes(s.cooldownMinutes);
      setWhatsappEnabled(s.whatsappEnabled);
      setWhatsappConnectionId(s.whatsappConnectionId);
      setWhatsappAlertPhone(s.whatsappAlertPhone || "");
      setEmailEnabled(s.emailEnabled);
      setSmtpHost(s.smtpHost || "");
      setSmtpPort(s.smtpPort || 587);
      setSmtpUser(s.smtpUser || "");
      setSmtpSecure(s.smtpSecure);
      setEmailFrom(s.emailFrom || "");
      setEmailTo(s.emailTo || "");
    }
  }, [settingsQuery.data]);

  const handleSave = () => {
    const body: NotificationSettingsUpdate = {
      enabled,
      failureThreshold,
      cooldownMinutes,
      whatsappEnabled,
      whatsappConnectionId,
      whatsappAlertPhone: whatsappAlertPhone || null,
      emailEnabled,
      smtpHost: smtpHost || null,
      smtpPort,
      smtpUser: smtpUser || null,
      smtpSecure,
      emailFrom: emailFrom || null,
      emailTo: emailTo || null,
    };
    // Only send the password when replaced — never clear it (R8).
    if (smtpPass.trim()) body.smtpPass = smtpPass.trim();
    updateMutation.mutate(body);
  };

  const failureStates = failureStatesQuery.data || {};
  const hasActiveFailures = Object.values(failureStates).some(
    (s) => s.consecutiveFailures > 0
  );

  if (settingsQuery.isLoading) {
    return (
      <div className="flex items-center justify-center py-20">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (settingsQuery.isError) {
    return (
      <div className="space-y-6">
        <div>
          <h1 className="text-2xl font-bold tracking-tight flex items-center gap-2">
            <Bell className="h-6 w-6 text-primary" />
            Notificações
          </h1>
        </div>
        <ErrorState
          message={
            settingsQuery.error instanceof Error
              ? settingsQuery.error.message
              : undefined
          }
          onRetry={() => settingsQuery.refetch()}
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight flex items-center gap-2">
            <Bell className="h-6 w-6 text-primary" />
            Notificações
          </h1>
          <p className="text-muted-foreground mt-1">
            Configure alertas automáticos para falhas de webhook
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            onClick={handleSave}
            disabled={updateMutation.isPending}
          >
            {updateMutation.isPending ? (
              <Loader2 className="h-4 w-4 mr-2 animate-spin" />
            ) : (
              <Save className="h-4 w-4 mr-2" />
            )}
            Salvar
          </Button>
        </div>
      </div>

      {/* Active Failures Banner */}
      {hasActiveFailures && (
        <Card className="border-destructive/50 bg-destructive/5">
          <CardContent className="py-3">
            <div className="flex items-center gap-3">
              <AlertTriangle className="h-5 w-5 text-destructive" />
              <div className="flex-1">
                <p className="text-sm font-medium text-destructive">Falhas ativas detectadas</p>
                <div className="flex flex-wrap gap-2 mt-1">
                  {Object.entries(failureStates).map(([connId, state]) => {
                    if (state.consecutiveFailures === 0) return null;
                    const conn = connectionsQuery.data?.find((c) => c.id === Number(connId));
                    return (
                      <Badge key={connId} variant="destructive" className="text-xs">
                        {conn?.name || `#${connId}`}: {state.consecutiveFailures} falha(s)
                      </Badge>
                    );
                  })}
                </div>
              </div>
            </div>
          </CardContent>
        </Card>
      )}

      <div className="grid gap-6 lg:grid-cols-2">
        {/* General Settings */}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-lg">
              <Settings2 className="h-5 w-5" />
              Configurações Gerais
            </CardTitle>
            <CardDescription>
              Controle quando os alertas são disparados
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-6">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label className="text-sm font-medium">Sistema de Notificações</Label>
                <p className="text-xs text-muted-foreground">
                  Ativar alertas automáticos para falhas
                </p>
              </div>
              <Switch checked={enabled} onCheckedChange={setEnabled} />
            </div>

            <Separator />

            <div className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="threshold">Limite de Falhas Consecutivas</Label>
                <Input
                  id="threshold"
                  type="number"
                  min={1}
                  max={100}
                  value={failureThreshold}
                  onChange={(e) => setFailureThreshold(Number(e.target.value))}
                  disabled={!enabled}
                />
                <p className="text-xs text-muted-foreground">
                  Alerta será enviado após {failureThreshold} falha(s) consecutiva(s)
                </p>
              </div>

              <div className="space-y-2">
                <Label htmlFor="cooldown">Cooldown entre Alertas (minutos)</Label>
                <Input
                  id="cooldown"
                  type="number"
                  min={1}
                  max={1440}
                  value={cooldownMinutes}
                  onChange={(e) => setCooldownMinutes(Number(e.target.value))}
                  disabled={!enabled}
                />
                <p className="text-xs text-muted-foreground">
                  Aguarda {cooldownMinutes} minuto(s) antes de enviar outro alerta para a mesma conexão
                </p>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* WhatsApp Channel */}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-lg">
              <MessageSquare className="h-5 w-5 text-green-500" />
              Canal WhatsApp
            </CardTitle>
            <CardDescription>
              Receba alertas diretamente no WhatsApp
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-6">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label className="text-sm font-medium">Alertas via WhatsApp</Label>
                <p className="text-xs text-muted-foreground">
                  Usa uma conexão do Bridge para enviar
                </p>
              </div>
              <Switch
                checked={whatsappEnabled}
                onCheckedChange={setWhatsappEnabled}
                disabled={!enabled}
              />
            </div>

            <Separator />

            <div className="space-y-4">
              <div className="space-y-2">
                <Label>Conexão para Envio</Label>
                <Select
                  value={whatsappConnectionId?.toString() || ""}
                  onValueChange={(v) => setWhatsappConnectionId(v ? Number(v) : null)}
                  disabled={!enabled || !whatsappEnabled}
                >
                  <SelectTrigger>
                    <SelectValue placeholder="Selecione uma conexão" />
                  </SelectTrigger>
                  <SelectContent>
                    {connectionsQuery.data?.map((conn) => (
                      <SelectItem key={conn.id} value={conn.id.toString()}>
                        {conn.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-xs text-muted-foreground">
                  Instância Evolution GO usada para enviar os alertas
                </p>
              </div>

              <div className="space-y-2">
                <Label htmlFor="waPhone">Telefone para Alertas</Label>
                <Input
                  id="waPhone"
                  placeholder="5511999999999"
                  value={whatsappAlertPhone}
                  onChange={(e) => setWhatsappAlertPhone(e.target.value)}
                  disabled={!enabled || !whatsappEnabled}
                />
                <p className="text-xs text-muted-foreground">
                  Número com DDI + DDD (sem +, espaços ou traços)
                </p>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Email Channel */}
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-lg">
              <Mail className="h-5 w-5 text-blue-500" />
              Canal E-mail (SMTP)
            </CardTitle>
            <CardDescription>
              Receba alertas por e-mail via servidor SMTP
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-6">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label className="text-sm font-medium">Alertas via E-mail</Label>
                <p className="text-xs text-muted-foreground">
                  Configure um servidor SMTP para envio
                </p>
              </div>
              <Switch
                checked={emailEnabled}
                onCheckedChange={setEmailEnabled}
                disabled={!enabled}
              />
            </div>

            <Separator />

            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="smtpHost">Servidor SMTP</Label>
                <Input
                  id="smtpHost"
                  placeholder="smtp.gmail.com"
                  value={smtpHost}
                  onChange={(e) => setSmtpHost(e.target.value)}
                  disabled={!enabled || !emailEnabled}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="smtpPort">Porta</Label>
                <Input
                  id="smtpPort"
                  type="number"
                  placeholder="587"
                  value={smtpPort}
                  onChange={(e) => setSmtpPort(Number(e.target.value))}
                  disabled={!enabled || !emailEnabled}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="smtpUser">Usuário SMTP</Label>
                <Input
                  id="smtpUser"
                  placeholder="seu@email.com"
                  value={smtpUser}
                  onChange={(e) => setSmtpUser(e.target.value)}
                  disabled={!enabled || !emailEnabled}
                />
              </div>
              <div className="space-y-2">
                <div className="flex items-center justify-between">
                  <Label htmlFor="smtpPass">Senha SMTP</Label>
                  <Badge
                    variant="outline"
                    className={`text-[10px] py-0 h-5 gap-1 ${settingsQuery.data?.hasSmtpPass ? "border-emerald-500/30 text-emerald-400" : "border-muted-foreground/30 text-muted-foreground"}`}
                  >
                    <KeyRound className="h-2.5 w-2.5" />
                    {settingsQuery.data?.hasSmtpPass ? "definida" : "ausente"}
                  </Badge>
                </div>
                <Input
                  id="smtpPass"
                  type="password"
                  placeholder={
                    settingsQuery.data?.hasSmtpPass
                      ? "Deixe em branco para manter"
                      : "Senha ou App Password"
                  }
                  value={smtpPass}
                  onChange={(e) => setSmtpPass(e.target.value)}
                  disabled={!enabled || !emailEnabled}
                  autoComplete="new-password"
                />
                <p className="text-xs text-muted-foreground">
                  A senha nunca é exibida. Preencha apenas para substituí-la.
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="emailFrom">E-mail Remetente</Label>
                <Input
                  id="emailFrom"
                  placeholder="alertas@suaempresa.com"
                  value={emailFrom}
                  onChange={(e) => setEmailFrom(e.target.value)}
                  disabled={!enabled || !emailEnabled}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="emailTo">E-mail Destinatário</Label>
                <Input
                  id="emailTo"
                  placeholder="admin@suaempresa.com"
                  value={emailTo}
                  onChange={(e) => setEmailTo(e.target.value)}
                  disabled={!enabled || !emailEnabled}
                />
              </div>
            </div>

            <div className="flex items-center gap-3">
              <Switch
                checked={smtpSecure}
                onCheckedChange={setSmtpSecure}
                disabled={!enabled || !emailEnabled}
              />
              <div>
                <Label className="text-sm font-medium">Conexão Segura (TLS/SSL)</Label>
                <p className="text-xs text-muted-foreground">
                  Ative para porta 465 (SSL). Para porta 587 (STARTTLS), deixe desativado.
                </p>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Alert History */}
        <Card className="lg:col-span-2">
          <CardHeader>
            <div className="flex items-center justify-between">
              <div>
                <CardTitle className="flex items-center gap-2 text-lg">
                  <History className="h-5 w-5" />
                  Histórico de Alertas
                </CardTitle>
                <CardDescription>
                  Últimos alertas enviados pelo sistema
                </CardDescription>
              </div>
              <Button
                variant="ghost"
                size="icon"
                onClick={() => alertsQuery.refetch()}
              >
                <RefreshCw className={`h-4 w-4 ${alertsQuery.isFetching ? "animate-spin" : ""}`} />
              </Button>
            </div>
          </CardHeader>
          <CardContent>
            {alertsQuery.isLoading ? (
              <div className="flex items-center justify-center py-8">
                <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
              </div>
            ) : alertsQuery.isError ? (
              <ErrorState
                message={
                  alertsQuery.error instanceof Error
                    ? alertsQuery.error.message
                    : undefined
                }
                onRetry={() => alertsQuery.refetch()}
              />
            ) : !alertsQuery.data?.alerts?.length ? (
              <div className="flex flex-col items-center justify-center py-8 text-muted-foreground">
                <BellOff className="h-10 w-10 mb-3 opacity-50" />
                <p className="text-sm">Nenhum alerta enviado ainda</p>
              </div>
            ) : (
              <div className="space-y-3">
                {alertsQuery.data.alerts.map((alert) => {
                  const conn = connectionsQuery.data?.find((c) => c.id === alert.connectionId);
                  return (
                    <div
                      key={alert.id}
                      className="flex items-start gap-3 p-3 rounded-lg border bg-card/50"
                    >
                      <div className="mt-0.5">
                        {alert.success ? (
                          <CheckCircle2 className="h-5 w-5 text-green-500" />
                        ) : (
                          <XCircle className="h-5 w-5 text-destructive" />
                        )}
                      </div>
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2 flex-wrap">
                          <Badge variant={alert.channel === "whatsapp" ? "default" : "secondary"} className="text-xs">
                            {alert.channel === "whatsapp" ? (
                              <><MessageSquare className="h-3 w-3 mr-1" /> WhatsApp</>
                            ) : (
                              <><Mail className="h-3 w-3 mr-1" /> E-mail</>
                            )}
                          </Badge>
                          <span className="text-xs text-muted-foreground">
                            {conn?.name || `Conexão #${alert.connectionId}`}
                          </span>
                          <span className="text-xs text-muted-foreground">
                            {alert.failureCount} falha(s)
                          </span>
                        </div>
                        {alert.errorMessage && (
                          <p className="text-xs text-destructive mt-1 truncate">
                            {alert.errorMessage}
                          </p>
                        )}
                        <p className="text-xs text-muted-foreground mt-1">
                          {new Date(alert.createdAt).toLocaleString("pt-BR")}
                        </p>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
