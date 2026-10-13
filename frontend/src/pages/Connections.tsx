import { api } from "@/api";
import type { ConnectionTestResult } from "@/api";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ErrorState } from "@/components/ErrorState";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Tooltip, TooltipContent, TooltipProvider, TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Plus, MoreVertical, Pencil, Trash2, TestTube, Wifi, WifiOff,
  Link2, Zap, MessageSquareText, Users, RefreshCw, CheckCircle2,
  XCircle, AlertCircle, ArrowRightLeft, KeyRound,
} from "lucide-react";
import { useState } from "react";
import { useLocation } from "wouter";
import { toast } from "sonner";

/** Inline webhook status indicator for a single connection */
function WebhookStatusIndicator({ connectionId }: { connectionId: number }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["connections", connectionId, "webhook-status"],
    queryFn: () => api.connections.webhookStatus(connectionId),
    refetchInterval: 60000,
    retry: 1,
  });

  if (isLoading) {
    return (
      <div className="flex items-center gap-1.5">
        <div className="h-2 w-2 rounded-full bg-muted-foreground/30 animate-pulse" />
        <span className="text-[10px] text-muted-foreground">Verificando...</span>
      </div>
    );
  }

  if (error || !data) {
    return (
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger asChild>
            <div className="flex items-center gap-1.5 cursor-help">
              <AlertCircle className="h-3.5 w-3.5 text-yellow-500" />
              <span className="text-[10px] text-yellow-500">Erro ao verificar</span>
            </div>
          </TooltipTrigger>
          <TooltipContent side="bottom" className="max-w-xs">
            <p className="text-xs">Não foi possível verificar o status dos webhooks.</p>
          </TooltipContent>
        </Tooltip>
      </TooltipProvider>
    );
  }

  const evOk = data.evolution.configured;
  const cwOk = data.chatwoot.configured;
  const bothOk = evOk && cwOk;
  const noneOk = !evOk && !cwOk;

  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <div className="flex items-center gap-2 cursor-help">
            {/* Evolution webhook indicator */}
            <div className="flex items-center gap-1">
              <div className={`h-2 w-2 rounded-full ${evOk ? "bg-emerald-500" : "bg-red-500"}`} />
              <span className={`text-[10px] font-medium ${evOk ? "text-emerald-500" : "text-red-400"}`}>EVO</span>
            </div>
            <ArrowRightLeft className="h-3 w-3 text-muted-foreground/50" />
            {/* Chatwoot webhook indicator */}
            <div className="flex items-center gap-1">
              <div className={`h-2 w-2 rounded-full ${cwOk ? "bg-emerald-500" : "bg-red-500"}`} />
              <span className={`text-[10px] font-medium ${cwOk ? "text-emerald-500" : "text-red-400"}`}>CW</span>
            </div>
            {/* Overall status icon */}
            {bothOk ? (
              <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500 ml-0.5" />
            ) : noneOk ? (
              <XCircle className="h-3.5 w-3.5 text-red-500 ml-0.5" />
            ) : (
              <AlertCircle className="h-3.5 w-3.5 text-yellow-500 ml-0.5" />
            )}
          </div>
        </TooltipTrigger>
        <TooltipContent side="bottom" className="max-w-sm">
          <div className="space-y-2 text-xs">
            <p className="font-semibold">Status dos Webhooks</p>
            <div className="flex items-center gap-2">
              {evOk ? <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" /> : <XCircle className="h-3.5 w-3.5 text-red-500" />}
              <span>Evolution → Bridge: {evOk ? "Configurado" : "Não configurado"}</span>
            </div>
            {data.evolution.urlMasked && (
              <p className="text-muted-foreground pl-5 truncate max-w-[250px]">{data.evolution.urlMasked}</p>
            )}
            <div className="flex items-center gap-2">
              {cwOk ? <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" /> : <XCircle className="h-3.5 w-3.5 text-red-500" />}
              <span>Chatwoot → Bridge: {cwOk ? "Configurado" : "Não configurado"}</span>
            </div>
            {data.chatwoot.urlMasked && (
              <p className="text-muted-foreground pl-5 truncate max-w-[250px]">{data.chatwoot.urlMasked}</p>
            )}
            {data.instanceStatus && data.instanceStatus !== "unknown" && (
              <div className="flex items-center gap-2 pt-1 border-t border-border/50">
                <Wifi className="h-3.5 w-3.5 text-muted-foreground" />
                <span>Instância: {data.instanceStatus}</span>
              </div>
            )}
            {!bothOk && (
              <p className="text-yellow-500 pt-1">Edite a conexão e configure os webhooks na aba Webhooks.</p>
            )}
          </div>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

/** Credentials are never returned by the API — show presence only. */
function SecretBadge({ label, present }: { label: string; present: boolean }) {
  return (
    <Badge
      variant="outline"
      className={`text-[10px] py-0 h-5 gap-1 ${present ? "border-emerald-500/30 text-emerald-400" : "border-muted-foreground/30 text-muted-foreground"}`}
    >
      <KeyRound className="h-2.5 w-2.5" />
      {label} {present ? "definida" : "ausente"}
    </Badge>
  );
}

export default function Connections() {
  const [, setLocation] = useLocation();
  const queryClient = useQueryClient();
  const {
    data: connections,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
    queryKey: ["connections"],
    queryFn: () => api.connections.list(),
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["connections"] });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.connections.remove(id),
    onSuccess: () => { toast.success("Conexão desativada"); invalidate(); },
    onError: (e: Error) => toast.error(`Erro: ${e.message}`),
  });
  const testMutation = useMutation({
    mutationFn: (id: number) => api.connections.test(id),
  });
  const toggleMutation = useMutation({
    mutationFn: ({ id, isActive }: { id: number; isActive: boolean }) =>
      api.connections.update(id, { isActive }),
    onSuccess: () => { invalidate(); },
    onError: (e: Error) => toast.error(`Erro: ${e.message}`),
  });
  const syncMutation = useMutation({
    mutationFn: (connectionId: number) => api.contacts.sync(connectionId),
    onSuccess: (result) => {
      toast.success(`Sincronização concluída: ${result.synced} contatos importados, ${result.skipped} ignorados, ${result.failed} falhas`);
    },
    onError: (e: Error) => toast.error(`Erro na sincronização: ${e.message}`),
  });

  const [deleteId, setDeleteId] = useState<number | null>(null);
  const [testResult, setTestResult] = useState<(ConnectionTestResult & { id: number }) | null>(null);
  const [testingId, setTestingId] = useState<number | null>(null);
  const [syncingId, setSyncingId] = useState<number | null>(null);

  const handleTest = async (id: number) => {
    setTestingId(id);
    setTestResult(null);
    try {
      const result = await testMutation.mutateAsync(id);
      setTestResult({ id, ...result });
      if (result.success) {
        toast.success("Conexão testada com sucesso!");
      } else {
        toast.error("Falha no teste da conexão");
      }
    } catch (e) {
      toast.error(`Erro no teste: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setTestingId(null);
    }
  };

  const handleSync = async (id: number) => {
    setSyncingId(id);
    try {
      await syncMutation.mutateAsync(id);
    } finally {
      setSyncingId(null);
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-6">
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">Conexões</h1>
            <p className="text-muted-foreground text-sm mt-1">Gerencie suas integrações</p>
          </div>
        </div>
        <div className="grid gap-4">
          {[1, 2].map(i => (
            <Card key={i}><CardContent className="p-5"><Skeleton className="h-24 w-full" /></CardContent></Card>
          ))}
        </div>
      </div>
    );
  }

  if (isError) {
    return (
      <div className="space-y-6">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Conexões</h1>
          <p className="text-muted-foreground text-sm mt-1">Gerencie suas integrações</p>
        </div>
        <ErrorState
          message={error instanceof Error ? error.message : undefined}
          onRetry={() => refetch()}
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Conexões</h1>
          <p className="text-muted-foreground text-sm mt-1">
            {connections?.length ?? 0} conexão(ões) configurada(s)
          </p>
        </div>
        <Button onClick={() => setLocation("/connections/new")} className="gap-2">
          <Plus className="h-4 w-4" />
          Nova Conexão
        </Button>
      </div>

      {!connections || connections.length === 0 ? (
        <Card className="border-dashed border-2">
          <CardContent className="flex flex-col items-center justify-center py-16">
            <div className="h-14 w-14 rounded-full bg-primary/10 flex items-center justify-center mb-4">
              <Link2 className="h-7 w-7 text-primary" />
            </div>
            <h3 className="text-lg font-semibold mb-1">Nenhuma conexão</h3>
            <p className="text-sm text-muted-foreground text-center max-w-sm mb-6">
              Crie sua primeira conexão para começar a sincronizar mensagens entre o WhatsApp e o Chatwoot.
            </p>
            <Button onClick={() => setLocation("/connections/new")} className="gap-2">
              <Plus className="h-4 w-4" />
              Criar Conexão
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-3">
          {connections.map((conn) => (
            <Card key={conn.id} className="border-border/50 hover:border-border transition-colors">
              <CardContent className="p-5">
                <div className="flex items-start justify-between gap-4">
                  <div className="flex items-start gap-4 min-w-0 flex-1">
                    <div className={`h-10 w-10 rounded-lg flex items-center justify-center shrink-0 ${conn.isActive ? "bg-emerald-500/10" : "bg-muted"}`}>
                      {conn.isActive ? (
                        <Wifi className="h-5 w-5 text-emerald-500" />
                      ) : (
                        <WifiOff className="h-5 w-5 text-muted-foreground" />
                      )}
                    </div>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2 mb-1 flex-wrap">
                        <h3 className="font-semibold truncate">{conn.name}</h3>
                        <Badge variant={conn.isActive ? "default" : "secondary"} className="text-xs shrink-0">
                          {conn.isActive ? "Ativa" : "Inativa"}
                        </Badge>
                      </div>

                      {/* Connection details */}
                      <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-1 mt-2">
                        <div className="flex items-center gap-2 text-xs text-muted-foreground">
                          <MessageSquareText className="h-3.5 w-3.5 shrink-0" />
                          <span className="truncate">EVO: {conn.evolutionInstanceName}</span>
                        </div>
                        <div className="flex items-center gap-2 text-xs text-muted-foreground">
                          <MessageSquareText className="h-3.5 w-3.5 shrink-0" />
                          <span className="truncate">
                            CW: Conta {conn.chatwootAccountId}
                            {conn.chatwootInboxId ? ` / Inbox ${conn.chatwootInboxId}` : ""}
                            {conn.chatwootInboxName ? ` (${conn.chatwootInboxName})` : ""}
                          </span>
                        </div>
                      </div>

                      {/* Webhook status indicators */}
                      {conn.isActive && (
                        <div className="mt-2.5 pt-2.5 border-t border-border/30">
                          <WebhookStatusIndicator connectionId={conn.id} />
                        </div>
                      )}

                      {/* Bridge behavior badges */}
                      <div className="flex flex-wrap gap-1.5 mt-2">
                        <SecretBadge label="EVO key" present={conn.hasEvolutionApiKey} />
                        <SecretBadge label="EVO token" present={conn.hasEvolutionInstanceToken} />
                        <SecretBadge label="CW token" present={conn.hasChatwootApiToken} />
                        {conn.ignoreGroups && (
                          <Badge variant="outline" className="text-[10px] py-0 h-5 gap-1 border-blue-500/30 text-blue-400">
                            Ignora Grupos
                          </Badge>
                        )}
                        {conn.signMessages && (
                          <Badge variant="outline" className="text-[10px] py-0 h-5 gap-1 border-purple-500/30 text-purple-400">
                            Msg Assinadas
                          </Badge>
                        )}
                        {conn.autoReadMessages && (
                          <Badge variant="outline" className="text-[10px] py-0 h-5 gap-1 border-cyan-500/30 text-cyan-400">
                            Auto-Leitura
                          </Badge>
                        )}
                        {conn.rejectCalls && (
                          <Badge variant="outline" className="text-[10px] py-0 h-5 gap-1 border-orange-500/30 text-orange-400">
                            Rejeita Chamadas
                          </Badge>
                        )}
                      </div>

                      {/* Test result */}
                      {testResult?.id === conn.id && (
                        <div className={`mt-3 p-3 rounded-lg text-xs ${testResult.success ? "bg-emerald-500/10 text-emerald-400" : "bg-red-500/10 text-red-400"}`}>
                          <p className="font-medium mb-1">{testResult.success ? "Teste OK" : "Teste falhou"}</p>
                          <p>Evolution: {testResult.evolution?.ok ? "Conectado" : testResult.evolution?.error || "Erro"}</p>
                          <p>Chatwoot: {testResult.chatwoot?.ok ? "Conectado" : testResult.chatwoot?.error || "Erro"}</p>
                        </div>
                      )}
                    </div>
                  </div>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="icon" className="h-8 w-8 shrink-0">
                        <MoreVertical className="h-4 w-4" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onClick={() => handleTest(conn.id)} disabled={testingId === conn.id}>
                        <TestTube className="mr-2 h-4 w-4" />
                        {testingId === conn.id ? "Testando..." : "Testar Conexão"}
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => toggleMutation.mutate({ id: conn.id, isActive: !conn.isActive })}>
                        {conn.isActive ? <WifiOff className="mr-2 h-4 w-4" /> : <Wifi className="mr-2 h-4 w-4" />}
                        {conn.isActive ? "Desativar" : "Ativar"}
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        onClick={() => handleSync(conn.id)}
                        disabled={syncingId === conn.id || !conn.chatwootInboxId}
                      >
                        {syncingId === conn.id ? (
                          <RefreshCw className="mr-2 h-4 w-4 animate-spin" />
                        ) : (
                          <Users className="mr-2 h-4 w-4" />
                        )}
                        {syncingId === conn.id ? "Sincronizando..." : "Sincronizar Contatos"}
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem onClick={() => setLocation(`/connections/${conn.id}/edit`)}>
                        <Pencil className="mr-2 h-4 w-4" />
                        Editar
                      </DropdownMenuItem>
                      <DropdownMenuItem onClick={() => setDeleteId(conn.id)} className="text-destructive focus:text-destructive">
                        <Trash2 className="mr-2 h-4 w-4" />
                        Desativar
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <AlertDialog open={deleteId !== null} onOpenChange={() => setDeleteId(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Desativar conexão?</AlertDialogTitle>
            <AlertDialogDescription>
              A conexão será desativada e o histórico de mensagens e mapeamentos de
              contatos será preservado. Nenhum dado é apagado.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancelar</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => { if (deleteId) deleteMutation.mutate(deleteId); setDeleteId(null); }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Desativar
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
