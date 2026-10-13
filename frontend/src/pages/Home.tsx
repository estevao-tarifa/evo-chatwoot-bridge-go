import { api } from "@/api";
import { useQuery } from "@tanstack/react-query";
import { ErrorState } from "@/components/ErrorState";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Link2, MessageSquare, AlertTriangle, Activity, ArrowRight, Zap,
  ArrowDownLeft, ArrowUpRight, BarChart3, PieChart as PieChartIcon,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { useLocation } from "wouter";
import { useMemo, useState } from "react";
import {
  AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
  BarChart, Bar, PieChart, Pie, Cell, Legend,
} from "recharts";

// ─── Chart Colors ──────────────────────────────────────────────────
const COLORS = {
  incoming: "#10b981",   // emerald-500
  outgoing: "#3b82f6",   // blue-500
  total: "#8b5cf6",      // violet-500
};

const PIE_COLORS = ["#10b981", "#3b82f6", "#f59e0b", "#ef4444", "#8b5cf6", "#ec4899", "#06b6d4", "#84cc16"];

// ─── Stat Card ─────────────────────────────────────────────────────
function StatCard({ title, value, icon: Icon, description, variant }: {
  title: string;
  value: number | string;
  icon: any;
  description?: string;
  variant?: "default" | "success" | "warning" | "destructive";
}) {
  const colorMap = {
    default: "text-primary bg-primary/10",
    success: "text-emerald-500 bg-emerald-500/10",
    warning: "text-amber-500 bg-amber-500/10",
    destructive: "text-red-500 bg-red-500/10",
  };
  const colors = colorMap[variant || "default"];

  return (
    <Card className="border-border/50">
      <CardContent className="p-5">
        <div className="flex items-start justify-between">
          <div className="space-y-2">
            <p className="text-sm text-muted-foreground">{title}</p>
            <p className="text-3xl font-bold tracking-tight">{value}</p>
            {description && (
              <p className="text-xs text-muted-foreground">{description}</p>
            )}
          </div>
          <div className={`h-10 w-10 rounded-lg flex items-center justify-center ${colors}`}>
            <Icon className="h-5 w-5" />
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

// ─── Direction / Status Badges ─────────────────────────────────────
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

// ─── Custom Tooltip ────────────────────────────────────────────────
function ChartTooltip({ active, payload, label }: any) {
  if (!active || !payload?.length) return null;
  return (
    <div className="bg-popover text-popover-foreground border border-border rounded-lg shadow-lg p-3 text-sm">
      <p className="font-medium mb-1">{label}</p>
      {payload.map((entry: any, i: number) => (
        <div key={i} className="flex items-center gap-2">
          <div className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: entry.color }} />
          <span className="text-muted-foreground">{entry.name}:</span>
          <span className="font-medium">{entry.value}</span>
        </div>
      ))}
    </div>
  );
}

// ─── Message Type Label ────────────────────────────────────────────
const TYPE_LABELS: Record<string, string> = {
  text: "Texto",
  image: "Imagem",
  audio: "Áudio",
  video: "Vídeo",
  document: "Documento",
  sticker: "Sticker",
  location: "Localização",
  contact: "Contato",
  reaction: "Reação",
  unknown: "Outro",
};

// ─── Main Dashboard ────────────────────────────────────────────────
export default function Home() {
  const {
    data: stats,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
    queryKey: ["dashboard", "stats"],
    queryFn: () => api.dashboard.stats(),
  });
  const { data: hourlyData } = useQuery({
    queryKey: ["dashboard", "messages-per-hour"],
    queryFn: () => api.dashboard.messagesPerHour(),
  });
  const { data: dailyData } = useQuery({
    queryKey: ["dashboard", "messages-per-day"],
    queryFn: () => api.dashboard.messagesPerDay(),
  });
  const { data: typeData } = useQuery({
    queryKey: ["dashboard", "message-types"],
    queryFn: () => api.dashboard.messageTypes(),
  });
  const [, setLocation] = useLocation();
  const [chartView, setChartView] = useState<"hourly" | "daily">("daily");

  // Format hourly data for chart
  const formattedHourly = useMemo(() => {
    if (!hourlyData || !Array.isArray(hourlyData)) return [];
    return (hourlyData as any[]).map((row: any) => ({
      label: new Date(row.hour).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" }),
      incoming: Number(row.incoming) || 0,
      outgoing: Number(row.outgoing) || 0,
      total: Number(row.total) || 0,
    }));
  }, [hourlyData]);

  // Format daily data for chart
  const formattedDaily = useMemo(() => {
    if (!dailyData || !Array.isArray(dailyData)) return [];
    return (dailyData as any[]).map((row: any) => ({
      label: new Date(row.day + "T12:00:00").toLocaleDateString("pt-BR", { day: "2-digit", month: "2-digit" }),
      incoming: Number(row.incoming) || 0,
      outgoing: Number(row.outgoing) || 0,
      total: Number(row.total) || 0,
    }));
  }, [dailyData]);

  // Format type data for pie chart
  const formattedTypes = useMemo(() => {
    if (!typeData || !Array.isArray(typeData)) return [];
    return (typeData as any[]).map((row: any) => ({
      name: TYPE_LABELS[row.type] || row.type || "Outro",
      value: Number(row.count) || 0,
    }));
  }, [typeData]);

  const chartData = chartView === "hourly" ? formattedHourly : formattedDaily;

  if (isLoading) {
    return (
      <div className="space-y-6">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Dashboard</h1>
          <p className="text-muted-foreground text-sm mt-1">Visão geral do Bridge WhatsApp</p>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
          {[1, 2, 3, 4].map(i => (
            <Card key={i}><CardContent className="p-5"><Skeleton className="h-20 w-full" /></CardContent></Card>
          ))}
        </div>
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
          <Card className="lg:col-span-2"><CardContent className="p-5"><Skeleton className="h-64 w-full" /></CardContent></Card>
          <Card><CardContent className="p-5"><Skeleton className="h-64 w-full" /></CardContent></Card>
        </div>
      </div>
    );
  }

  if (isError) {
    return (
      <div className="space-y-6">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Dashboard</h1>
          <p className="text-muted-foreground text-sm mt-1">Visão geral do Bridge WhatsApp</p>
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
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Dashboard</h1>
          <p className="text-muted-foreground text-sm mt-1">Visão geral do Bridge WhatsApp</p>
        </div>
        <Button onClick={() => setLocation("/connections/new")} className="gap-2">
          <Zap className="h-4 w-4" />
          Nova Conexão
        </Button>
      </div>

      {/* Stat Cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <StatCard
          title="Conexões Ativas"
          value={stats?.activeConnections ?? 0}
          icon={Link2}
          description={`${stats?.totalConnections ?? 0} total`}
          variant="success"
        />
        <StatCard
          title="Mensagens Hoje"
          value={stats?.todayMessages ?? 0}
          icon={MessageSquare}
          variant="default"
        />
        <StatCard
          title="Total de Mensagens"
          value={stats?.totalMessages ?? 0}
          icon={Activity}
        />
        <StatCard
          title="Falhas"
          value={stats?.failedMessages ?? 0}
          icon={AlertTriangle}
          variant={(stats?.failedMessages ?? 0) > 0 ? "destructive" : "default"}
        />
      </div>

      {/* Charts Row */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        {/* Volume Chart */}
        <Card className="lg:col-span-2 border-border/50">
          <CardHeader className="pb-2">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <BarChart3 className="h-4 w-4 text-muted-foreground" />
                <CardTitle className="text-base font-semibold">Volume de Mensagens</CardTitle>
              </div>
              <div className="flex gap-1">
                <Button
                  variant={chartView === "hourly" ? "default" : "ghost"}
                  size="sm"
                  className="text-xs h-7 px-2.5"
                  onClick={() => setChartView("hourly")}
                >
                  24h
                </Button>
                <Button
                  variant={chartView === "daily" ? "default" : "ghost"}
                  size="sm"
                  className="text-xs h-7 px-2.5"
                  onClick={() => setChartView("daily")}
                >
                  30 dias
                </Button>
              </div>
            </div>
          </CardHeader>
          <CardContent className="pt-0">
            {chartData.length === 0 ? (
              <div className="flex flex-col items-center justify-center py-16 text-center">
                <BarChart3 className="h-10 w-10 text-muted-foreground/30 mb-3" />
                <p className="text-sm text-muted-foreground">Sem dados para o período selecionado</p>
                <p className="text-xs text-muted-foreground mt-1">Os gráficos aparecerão quando houver mensagens processadas</p>
              </div>
            ) : (
              <ResponsiveContainer width="100%" height={280}>
                <AreaChart data={chartData} margin={{ top: 5, right: 10, left: -10, bottom: 0 }}>
                  <defs>
                    <linearGradient id="gradIncoming" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor={COLORS.incoming} stopOpacity={0.3} />
                      <stop offset="95%" stopColor={COLORS.incoming} stopOpacity={0} />
                    </linearGradient>
                    <linearGradient id="gradOutgoing" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor={COLORS.outgoing} stopOpacity={0.3} />
                      <stop offset="95%" stopColor={COLORS.outgoing} stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" opacity={0.3} />
                  <XAxis
                    dataKey="label"
                    tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }}
                    tickLine={false}
                    axisLine={false}
                    interval="preserveStartEnd"
                  />
                  <YAxis
                    tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }}
                    tickLine={false}
                    axisLine={false}
                    allowDecimals={false}
                  />
                  <Tooltip content={<ChartTooltip />} />
                  <Area
                    type="monotone"
                    dataKey="incoming"
                    name="WA → CW"
                    stroke={COLORS.incoming}
                    fill="url(#gradIncoming)"
                    strokeWidth={2}
                  />
                  <Area
                    type="monotone"
                    dataKey="outgoing"
                    name="CW → WA"
                    stroke={COLORS.outgoing}
                    fill="url(#gradOutgoing)"
                    strokeWidth={2}
                  />
                </AreaChart>
              </ResponsiveContainer>
            )}
            {/* Legend */}
            {chartData.length > 0 && (
              <div className="flex items-center justify-center gap-6 mt-2 text-xs text-muted-foreground">
                <div className="flex items-center gap-1.5">
                  <div className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: COLORS.incoming }} />
                  <span>WhatsApp → Chatwoot</span>
                </div>
                <div className="flex items-center gap-1.5">
                  <div className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: COLORS.outgoing }} />
                  <span>Chatwoot → WhatsApp</span>
                </div>
              </div>
            )}
          </CardContent>
        </Card>

        {/* Message Type Distribution */}
        <Card className="border-border/50">
          <CardHeader className="pb-2">
            <div className="flex items-center gap-2">
              <PieChartIcon className="h-4 w-4 text-muted-foreground" />
              <CardTitle className="text-base font-semibold">Tipos de Mensagem</CardTitle>
            </div>
          </CardHeader>
          <CardContent className="pt-0">
            {formattedTypes.length === 0 ? (
              <div className="flex flex-col items-center justify-center py-16 text-center">
                <PieChartIcon className="h-10 w-10 text-muted-foreground/30 mb-3" />
                <p className="text-sm text-muted-foreground">Sem dados</p>
              </div>
            ) : (
              <>
                <ResponsiveContainer width="100%" height={200}>
                  <PieChart>
                    <Pie
                      data={formattedTypes}
                      cx="50%"
                      cy="50%"
                      innerRadius={50}
                      outerRadius={80}
                      paddingAngle={3}
                      dataKey="value"
                    >
                      {formattedTypes.map((_: any, index: number) => (
                        <Cell key={index} fill={PIE_COLORS[index % PIE_COLORS.length]} />
                      ))}
                    </Pie>
                    <Tooltip
                      content={({ active, payload }: any) => {
                        if (!active || !payload?.length) return null;
                        return (
                          <div className="bg-popover text-popover-foreground border border-border rounded-lg shadow-lg p-2.5 text-sm">
                            <div className="flex items-center gap-2">
                              <div className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: payload[0].payload.fill }} />
                              <span>{payload[0].name}:</span>
                              <span className="font-medium">{payload[0].value}</span>
                            </div>
                          </div>
                        );
                      }}
                    />
                  </PieChart>
                </ResponsiveContainer>
                <div className="grid grid-cols-2 gap-1.5 mt-2">
                  {formattedTypes.slice(0, 6).map((type: any, i: number) => (
                    <div key={type.name} className="flex items-center gap-1.5 text-xs">
                      <div className="h-2 w-2 rounded-full shrink-0" style={{ backgroundColor: PIE_COLORS[i % PIE_COLORS.length] }} />
                      <span className="text-muted-foreground truncate">{type.name}</span>
                      <span className="font-medium ml-auto">{type.value}</span>
                    </div>
                  ))}
                </div>
              </>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Recent Activity */}
      <Card className="border-border/50">
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between">
            <CardTitle className="text-base font-semibold">Atividade Recente</CardTitle>
            <Button variant="ghost" size="sm" onClick={() => setLocation("/logs")} className="gap-1 text-xs text-muted-foreground">
              Ver tudo <ArrowRight className="h-3 w-3" />
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {!stats?.recentLogs || stats.recentLogs.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-12 text-center">
              <div className="h-12 w-12 rounded-full bg-muted flex items-center justify-center mb-3">
                <MessageSquare className="h-6 w-6 text-muted-foreground" />
              </div>
              <p className="text-sm text-muted-foreground">Nenhuma mensagem processada ainda</p>
              <p className="text-xs text-muted-foreground mt-1">As mensagens aparecerão aqui quando o Bridge estiver ativo</p>
            </div>
          ) : (
            <div className="space-y-1">
              {stats.recentLogs.map((log: any) => (
                <div key={log.id} className="flex items-center gap-3 py-2.5 px-3 rounded-lg hover:bg-muted/50 transition-colors">
                  <DirectionBadge direction={log.direction} />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium truncate">
                        {log.contactName || log.contactPhone || "Desconhecido"}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {log.contactPhone}
                      </span>
                    </div>
                    <p className="text-xs text-muted-foreground truncate mt-0.5">
                      {log.contentPreview || `[${log.messageType}]`}
                    </p>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <StatusBadge status={log.status} />
                    <span className="text-xs text-muted-foreground">
                      {log.createdAt ? new Date(log.createdAt).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" }) : ""}
                    </span>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
