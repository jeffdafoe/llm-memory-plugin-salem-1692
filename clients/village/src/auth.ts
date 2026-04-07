// Auth against llm-memory API
// Login: admin/login with password → web session token
// Registration: access-request → register → auto-login

const TOKEN_KEY = "session_token";
const AGENT_KEY = "agent_name";

// Get stored token from localStorage
export function getToken(): string | null {
    return localStorage.getItem(TOKEN_KEY);
}

export function getAgentName(): string | null {
    return localStorage.getItem(AGENT_KEY);
}

// Store session in localStorage
function setSession(agent: string, token: string): void {
    localStorage.setItem(TOKEN_KEY, token);
    localStorage.setItem(AGENT_KEY, agent);
}

// Clear stored session
export function clearSession(): void {
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(AGENT_KEY);
}

// Log in with agent name and password (web session, same as admin dashboard)
export async function login(username: string, password: string): Promise<void> {
    const response = await fetch("/llm/admin/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password }),
    });

    if (!response.ok) {
        const data = await response.json().catch(() => null);
        if (response.status === 401) {
            throw new Error("Invalid username or password");
        }
        if (data && data.error) {
            throw new Error(data.error.message || data.error);
        }
        throw new Error("Login failed");
    }

    const data = await response.json();
    setSession(username, data.session_token);
}

// Register a new account via llm-memory's three-step flow:
// 1. POST /api/access-request (get auto-approved invite code)
// 2. POST /api/register (create agent with invite code)
// 3. Auto-login with password
export async function register(name: string, password: string, email: string): Promise<void> {
    // Step 1: Get an invite code via access request
    const accessResponse = await fetch("/llm/api/access-request", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
            email,
            usage: "ZBBS Village registration",
        }),
    });

    if (!accessResponse.ok) {
        const data = await accessResponse.json().catch(() => null);
        if (data && data.error) {
            throw new Error(data.error);
        }
        throw new Error("Registration failed");
    }

    const accessData = await accessResponse.json();
    if (!accessData.auto_approved || !accessData.code) {
        throw new Error("Registration is not currently available");
    }

    // Step 2: Create the agent with the invite code
    const registerResponse = await fetch("/llm/api/register", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
            code: accessData.code,
            name,
            password,
        }),
    });

    if (!registerResponse.ok) {
        const data = await registerResponse.json().catch(() => null);
        if (data && data.error) {
            throw new Error(data.error);
        }
        throw new Error("Registration failed");
    }

    // Step 3: Auto-login with the password they just set
    await login(name, password);
}

// Verify the stored token is still valid
export async function verifyToken(): Promise<boolean> {
    const token = getToken();
    if (!token) {
        return false;
    }

    // Hit the dashboard endpoint to verify the web session is still valid
    const response = await fetch("/llm/admin/dashboard", {
        method: "POST",
        headers: {
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
        },
    });

    if (!response.ok) {
        clearSession();
        return false;
    }

    return true;
}
