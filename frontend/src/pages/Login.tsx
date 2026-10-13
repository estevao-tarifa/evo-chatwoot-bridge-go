import { useEffect, useState } from "react";
import { api, ApiClientError } from "@/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { toast } from "sonner";
import { MessageSquare, Lock, User, Eye, EyeOff, Loader2 } from "lucide-react";

type AuthState = "loading" | "needs_setup" | "login";

export default function Login({ onSuccess }: { onSuccess: () => void }) {
  const [authState, setAuthState] = useState<AuthState>("loading");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    api.auth
      .state()
      .then(data => {
        setAuthState(data.state === "needs_setup" ? "needs_setup" : "login");
      })
      .catch(() => setAuthState("login"));
  }, []);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!username || !password) {
      toast.error("Preencha todos os campos");
      return;
    }

    setLoading(true);
    try {
      if (authState === "needs_setup") {
        await api.auth.setup({
          username,
          password,
          displayName: displayName || undefined,
        });
        toast.success("Conta criada com sucesso!");
      } else {
        await api.auth.login({ username, password });
        toast.success("Login realizado!");
      }
      onSuccess();
    } catch (error) {
      const message =
        error instanceof ApiClientError ? error.message : "Falha na conexão";
      toast.error(message || "Erro ao autenticar");
    } finally {
      setLoading(false);
    }
  };

  if (authState === "loading") {
    return (
      <div className="flex items-center justify-center min-h-screen bg-background">
        <Loader2 className="h-8 w-8 animate-spin text-primary" />
      </div>
    );
  }

  const isSetup = authState === "needs_setup";

  return (
    <div className="flex items-center justify-center min-h-screen bg-background">
      <div className="w-full max-w-md px-4">
        {/* Logo & Title */}
        <div className="flex flex-col items-center mb-8">
          <div className="h-16 w-16 rounded-2xl bg-primary/10 flex items-center justify-center mb-4">
            <MessageSquare className="h-8 w-8 text-primary" />
          </div>
          <h1 className="text-2xl font-bold tracking-tight">Bridge WhatsApp</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Evolution GO ↔ Chatwoot
          </p>
        </div>

        <Card className="border-border/50">
          <CardHeader className="text-center pb-4">
            <CardTitle className="text-lg">
              {isSetup ? "Configuração Inicial" : "Entrar"}
            </CardTitle>
            <CardDescription>
              {isSetup
                ? "Crie sua conta de administrador para acessar o painel"
                : "Faça login para acessar o painel de gerenciamento"
              }
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleSubmit} className="space-y-4">
              {isSetup && (
                <div className="space-y-2">
                  <label className="text-sm font-medium text-foreground">Nome de Exibição</label>
                  <div className="relative">
                    <User className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
                    <Input
                      type="text"
                      placeholder="Seu nome"
                      value={displayName}
                      onChange={e => setDisplayName(e.target.value)}
                      className="pl-10"
                    />
                  </div>
                </div>
              )}

              <div className="space-y-2">
                <label className="text-sm font-medium text-foreground">Usuário</label>
                <div className="relative">
                  <User className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
                  <Input
                    type="text"
                    placeholder="admin"
                    value={username}
                    onChange={e => setUsername(e.target.value)}
                    className="pl-10"
                    autoComplete="username"
                    required
                  />
                </div>
              </div>

              <div className="space-y-2">
                <label className="text-sm font-medium text-foreground">Senha</label>
                <div className="relative">
                  <Lock className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
                  <Input
                    type={showPassword ? "text" : "password"}
                    placeholder={isSetup ? "Mínimo 6 caracteres" : "Sua senha"}
                    value={password}
                    onChange={e => setPassword(e.target.value)}
                    className="pl-10 pr-10"
                    autoComplete={isSetup ? "new-password" : "current-password"}
                    required
                    minLength={isSetup ? 6 : undefined}
                  />
                  <button
                    type="button"
                    onClick={() => setShowPassword(!showPassword)}
                    className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
                  >
                    {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                  </button>
                </div>
              </div>

              <Button type="submit" className="w-full" disabled={loading}>
                {loading ? (
                  <Loader2 className="h-4 w-4 animate-spin mr-2" />
                ) : null}
                {isSetup ? "Criar Conta" : "Entrar"}
              </Button>
            </form>

            {isSetup && (
              <p className="text-xs text-muted-foreground text-center mt-4">
                Você também pode definir <code className="text-xs bg-muted px-1 py-0.5 rounded">ADMIN_USER</code> e{" "}
                <code className="text-xs bg-muted px-1 py-0.5 rounded">ADMIN_PASS</code> no docker-compose.yml
              </p>
            )}
          </CardContent>
        </Card>

        <p className="text-xs text-muted-foreground text-center mt-6">
          Bridge WhatsApp
        </p>
      </div>
    </div>
  );
}
