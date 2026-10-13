import { api } from "@/api";
import { useQuery } from "@tanstack/react-query";
import { ErrorState } from "@/components/ErrorState";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import {
  ScrollText, Search, ArrowDownLeft, ArrowUpRight,
  ChevronLeft, ChevronRight, Eye, X,
} from "lucide-react";
import { useState, useMemo } from "react";

function DirectionBadge({ direction }: { direction: string }) {
  if (direction === "whatsapp_to_chatwoot") {
    return (
      <Badge variant="outline" className="gap-1 text-xs font-normal border-emerald-500/30 text-emerald-500">
        <ArrowDownLeft className="h-3 w-3" />
        WA → CW
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="gap-1 text-xs font-normal border-blue-500/30 text-blue-500">
      <ArrowUpRight className="h-3 w-3" />
      CW → WA
    </Badge>
  );
}

function StatusBadge({ status }: { status: string }) {
  const map: Record<string, { variant: "default" | "secondary" | "destructive" | "outline"; label: string }> = {
    sent: { variant: "default", label: "Enviada" },
    processing: { variant: "secondary", label: "Processando" },
    failed: { variant: "destructive", label: "Falha" },
    received: { variant: "outline", label: "Recebida" },
  };
  const config = map[status] || { variant: "outline" as const, label: status };
  return <Badge variant={config.variant} className="text-xs">{config.label}</Badge>;
}

export default function Logs() {
  const [filters, setFilters] = useState({
    direction: undefined as "whatsapp_to_chatwoot" | "chatwoot_to_whatsapp" | undefined,
    status: undefined as "received" | "processing" | "sent" | "failed" | undefined,
    search: "",
    limit: 25,
    offset: 0,
  });
  const [searchInput, setSearchInput] = useState("");
  const [selectedLog, setSelectedLog] = useState<any>(null);

  const queryInput = useMemo(() => ({
    ...filters,
    search: filters.search || undefined,
  }), [filters]);

  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["logs", queryInput],
    queryFn: () => api.logs.list(queryInput),
  });

  const handleSearch = () => {
    setFilters(f => ({ ...f, search: searchInput, offset: 0 }));
  };

  const totalPages = Math.ceil((data?.total ?? 0) / filters.limit);
  const currentPage = Math.floor(filters.offset / filters.limit) + 1;

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Logs de Mensagens</h1>
        <p className="text-muted-foreground text-sm mt-1">
          {data?.total ?? 0} mensagem(ns) registrada(s)
        </p>
      </div>

      <Card className="border-border/50">
        <CardContent className="p-4">
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-2 flex-1 min-w-[200px]">
              <div className="relative flex-1">
                <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
                <Input
                  placeholder="Buscar por telefone, nome ou conteúdo..."
                  value={searchInput}
                  onChange={(e) => setSearchInput(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && handleSearch()}
                  className="pl-9"
                />
              </div>
              <Button variant="outline" size="icon" onClick={handleSearch}>
                <Search className="h-4 w-4" />
              </Button>
            </div>
            <Select
              value={filters.direction || "all"}
              onValueChange={(v) => setFilters(f => ({ ...f, direction: v === "all" ? undefined : v as any, offset: 0 }))}
            >
              <SelectTrigger className="w-[160px]">
                <SelectValue placeholder="Direção" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">Todas direções</SelectItem>
                <SelectItem value="whatsapp_to_chatwoot">WA → CW</SelectItem>
                <SelectItem value="chatwoot_to_whatsapp">CW → WA</SelectItem>
              </SelectContent>
            </Select>
            <Select
              value={filters.status || "all"}
              onValueChange={(v) => setFilters(f => ({ ...f, status: v === "all" ? undefined : v as any, offset: 0 }))}
            >
              <SelectTrigger className="w-[140px]">
                <SelectValue placeholder="Status" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">Todos status</SelectItem>
                <SelectItem value="sent">Enviada</SelectItem>
                <SelectItem value="processing">Processando</SelectItem>
                <SelectItem value="failed">Falha</SelectItem>
                <SelectItem value="received">Recebida</SelectItem>
              </SelectContent>
            </Select>
            {(filters.direction || filters.status || filters.search) && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => { setFilters({ direction: undefined, status: undefined, search: "", limit: 25, offset: 0 }); setSearchInput(""); }}
                className="gap-1 text-xs"
              >
                <X className="h-3 w-3" /> Limpar
              </Button>
            )}
          </div>
        </CardContent>
      </Card>

      <Card className="border-border/50">
        <CardContent className="p-0">
          {isLoading ? (
            <div className="p-4 space-y-3">
              {[1, 2, 3, 4, 5].map(i => <Skeleton key={i} className="h-14 w-full" />)}
            </div>
          ) : isError ? (
            <div className="p-4">
              <ErrorState
                message={error instanceof Error ? error.message : undefined}
                onRetry={() => refetch()}
              />
            </div>
          ) : !data?.logs || data.logs.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-16">
              <div className="h-12 w-12 rounded-full bg-muted flex items-center justify-center mb-3">
                <ScrollText className="h-6 w-6 text-muted-foreground" />
              </div>
              <p className="text-sm text-muted-foreground">Nenhum log encontrado</p>
            </div>
          ) : (
            <div className="divide-y divide-border/50">
              {data.logs.map((log: any) => (
                <div
                  key={log.id}
                  className="flex items-center gap-3 py-3 px-4 hover:bg-muted/30 transition-colors cursor-pointer"
                  onClick={() => setSelectedLog(log)}
                >
                  <DirectionBadge direction={log.direction} />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium truncate">
                        {log.contactName || log.contactPhone || "Desconhecido"}
                      </span>
                      {log.contactPhone && (
                        <span className="text-xs text-muted-foreground">{log.contactPhone}</span>
                      )}
                    </div>
                    <p className="text-xs text-muted-foreground truncate mt-0.5">
                      {log.contentPreview || `[${log.messageType}]`}
                    </p>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <Badge variant="outline" className="text-xs">{log.messageType}</Badge>
                    <StatusBadge status={log.status} />
                    <span className="text-xs text-muted-foreground min-w-[60px] text-right">
                      {log.createdAt ? new Date(log.createdAt).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" }) : ""}
                    </span>
                    <Button variant="ghost" size="icon" className="h-7 w-7" onClick={(e) => { e.stopPropagation(); setSelectedLog(log); }}>
                      <Eye className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {totalPages > 1 && (
        <div className="flex items-center justify-between">
          <p className="text-sm text-muted-foreground">
            Página {currentPage} de {totalPages}
          </p>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={filters.offset === 0}
              onClick={() => setFilters(f => ({ ...f, offset: Math.max(0, f.offset - f.limit) }))}
            >
              <ChevronLeft className="h-4 w-4" />
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={currentPage >= totalPages}
              onClick={() => setFilters(f => ({ ...f, offset: f.offset + f.limit }))}
            >
              <ChevronRight className="h-4 w-4" />
            </Button>
          </div>
        </div>
      )}

      <Dialog open={!!selectedLog} onOpenChange={() => setSelectedLog(null)}>
        <DialogContent className="max-w-2xl max-h-[80vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>Detalhes do Log #{selectedLog?.id}</DialogTitle>
          </DialogHeader>
          {selectedLog && (
            <div className="space-y-4">
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <p className="text-xs text-muted-foreground mb-1">Direção</p>
                  <DirectionBadge direction={selectedLog.direction} />
                </div>
                <div>
                  <p className="text-xs text-muted-foreground mb-1">Status</p>
                  <StatusBadge status={selectedLog.status} />
                </div>
                <div>
                  <p className="text-xs text-muted-foreground mb-1">Tipo</p>
                  <Badge variant="outline">{selectedLog.messageType}</Badge>
                </div>
                <div>
                  <p className="text-xs text-muted-foreground mb-1">Contato</p>
                  <p className="text-sm">{selectedLog.contactName || "-"} ({selectedLog.contactPhone || "-"})</p>
                </div>
                <div>
                  <p className="text-xs text-muted-foreground mb-1">ID Origem</p>
                  <p className="text-xs font-mono break-all">{selectedLog.sourceMessageId || "-"}</p>
                </div>
                <div>
                  <p className="text-xs text-muted-foreground mb-1">ID Destino</p>
                  <p className="text-xs font-mono break-all">{selectedLog.destinationMessageId || "-"}</p>
                </div>
                <div className="col-span-2">
                  <p className="text-xs text-muted-foreground mb-1">Conteúdo</p>
                  <p className="text-sm bg-muted/50 p-3 rounded-lg">{selectedLog.contentPreview || "-"}</p>
                </div>
                {selectedLog.errorMessage && (
                  <div className="col-span-2">
                    <p className="text-xs text-muted-foreground mb-1">Erro</p>
                    <p className="text-sm bg-red-500/10 text-red-400 p-3 rounded-lg">{selectedLog.errorMessage}</p>
                  </div>
                )}
                <div>
                  <p className="text-xs text-muted-foreground mb-1">Criado em</p>
                  <p className="text-sm">{selectedLog.createdAt ? new Date(selectedLog.createdAt).toLocaleString("pt-BR") : "-"}</p>
                </div>
                <div>
                  <p className="text-xs text-muted-foreground mb-1">Processado em</p>
                  <p className="text-sm">{selectedLog.processedAt ? new Date(selectedLog.processedAt).toLocaleString("pt-BR") : "-"}</p>
                </div>
              </div>
              {selectedLog.rawPayload && (
                <div>
                  <p className="text-xs text-muted-foreground mb-1">Payload (JSON)</p>
                  <pre className="text-xs bg-muted/50 p-3 rounded-lg overflow-x-auto max-h-60">
                    {JSON.stringify(selectedLog.rawPayload, null, 2)}
                  </pre>
                </div>
              )}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
