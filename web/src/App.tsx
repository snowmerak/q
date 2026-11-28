import { useEffect, useMemo, useState } from "react";
import { BookOpen, MessagesSquare, Monitor, Moon, Settings, Sun, Users } from "lucide-react";
import { QClient } from "@/client/api";
import { ThemeProvider, useTheme, type Theme } from "@/components/theme-provider";
import { CharactersView } from "@/features/characters/CharactersView";
import { KnowledgeView } from "@/features/knowledge/KnowledgeView";
import { RoomsView } from "@/features/rooms/RoomsView";
import { SettingsView } from "@/features/settings/SettingsView";
import { cn } from "@/lib/utils";

type NavSection = "rooms" | "characters" | "knowledge" | "settings";

function ThemeSwitcher() {
  const { theme, setTheme } = useTheme();
  const nextTheme: Record<Theme, Theme> = {
    system: "light",
    light: "dark",
    dark: "system",
  };
  const Icon = theme === "dark" ? Moon : theme === "light" ? Sun : Monitor;

  return (
    <button
      type="button"
      title={`Theme: ${theme} (click to switch)`}
      onClick={() => setTheme(nextTheme[theme])}
      className="flex h-10 w-10 items-center justify-center rounded-lg border border-border bg-background text-muted-foreground hover:bg-accent hover:text-foreground transition-colors cursor-pointer"
    >
      <Icon className="h-4 w-4" />
    </button>
  );
}

function WorkspaceShell() {
  const client = useMemo(() => new QClient(), []);
  const [section, setSection] = useState<NavSection>("rooms");
  const [healthy, setHealthy] = useState<boolean | null>(null);

  useEffect(() => {
    let active = true;
    const check = async () => {
      try {
        const h = await client.health();
        if (active) setHealthy(h.status === "ready");
      } catch {
        if (active) setHealthy(false);
      }
    };
    void check();
    const timer = setInterval(() => void check(), 15000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [client]);

  const navItems: Array<{ id: NavSection; label: string; icon: typeof MessagesSquare }> = [
    { id: "rooms", label: "Rooms", icon: MessagesSquare },
    { id: "characters", label: "Characters", icon: Users },
    { id: "knowledge", label: "Knowledge & Skills", icon: BookOpen },
    { id: "settings", label: "Settings", icon: Settings },
  ];

  return (
    <div className="flex h-screen w-screen overflow-hidden bg-background text-foreground">
      <nav className="flex w-16 shrink-0 flex-col items-center justify-between border-r border-border bg-card py-4">
        <div className="flex flex-col items-center gap-3">
          <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-primary text-primary-foreground font-bold text-base shadow-xs">
            Q
          </div>
          <div className="mt-2 flex flex-col gap-1.5">
            {navItems.map((item) => {
              const Icon = item.icon;
              const active = section === item.id;
              return (
                <button
                  key={item.id}
                  type="button"
                  title={item.label}
                  onClick={() => setSection(item.id)}
                  className={cn(
                    "flex h-10 w-10 items-center justify-center rounded-lg transition-colors cursor-pointer",
                    active
                      ? "bg-primary/14 text-primary"
                      : "text-muted-foreground hover:bg-accent hover:text-foreground",
                  )}
                >
                  <Icon className="h-5 w-5" />
                </button>
              );
            })}
          </div>
        </div>

        <div className="flex flex-col items-center gap-3">
          <div
            title={healthy === null ? "Checking…" : healthy ? "System Ready" : "System Degraded"}
            className={cn(
              "h-2.5 w-2.5 rounded-full",
              healthy === null
                ? "bg-muted-foreground"
                : healthy
                  ? "bg-emerald-500"
                  : "bg-amber-500",
            )}
          />
          <ThemeSwitcher />
        </div>
      </nav>

      <div className="flex min-w-0 flex-1 overflow-hidden">
        {section === "rooms" && <RoomsView client={client} />}
        {section === "characters" && <CharactersView client={client} />}
        {section === "knowledge" && <KnowledgeView client={client} />}
        {section === "settings" && <SettingsView client={client} />}
      </div>
    </div>
  );
}

export function App() {
  return (
    <ThemeProvider>
      <WorkspaceShell />
    </ThemeProvider>
  );
}

export default App;
