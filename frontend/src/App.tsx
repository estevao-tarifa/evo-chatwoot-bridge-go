import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import NotFound from "@/pages/NotFound";
import { Route, Switch } from "wouter";
import ErrorBoundary from "./components/ErrorBoundary";
import { ThemeProvider } from "./contexts/ThemeContext";
import DashboardLayout from "./components/DashboardLayout";
import Home from "./pages/Home";
import Connections from "./pages/Connections";
import ConnectionForm from "./pages/ConnectionForm";
import Logs from "./pages/Logs";
import Notifications from "./pages/Notifications";

function Router() {
  return (
    <DashboardLayout>
      <Switch>
        <Route path={"/"} component={Home} />
        <Route path={"/connections"} component={Connections} />
        <Route path={"/connections/new"} component={ConnectionForm} />
        <Route path={"/connections/:id/edit"} component={ConnectionForm} />
        <Route path={"/logs"} component={Logs} />
        <Route path={"/notifications"} component={Notifications} />
        <Route path={"/404"} component={NotFound} />
        <Route component={NotFound} />
      </Switch>
    </DashboardLayout>
  );
}

function App() {
  return (
    <ErrorBoundary>
      <ThemeProvider defaultTheme="dark">
        <TooltipProvider>
          <Toaster />
          <Router />
        </TooltipProvider>
      </ThemeProvider>
    </ErrorBoundary>
  );
}

export default App;
