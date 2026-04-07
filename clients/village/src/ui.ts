// Login and registration UI — plain DOM, no framework

import { login, register } from "./auth";

type AuthCallback = () => void;

export function showLoginScreen(container: HTMLElement, onSuccess: AuthCallback): void {
    container.innerHTML = "";

    const wrapper = document.createElement("div");
    wrapper.className = "auth-wrapper";

    const box = document.createElement("div");
    box.className = "auth-box";

    const title = document.createElement("h1");
    title.textContent = "ZBBS Village";
    box.appendChild(title);

    const subtitle = document.createElement("p");
    subtitle.className = "auth-subtitle";
    subtitle.textContent = "Salem, 1692";
    box.appendChild(subtitle);

    const errorEl = document.createElement("div");
    errorEl.className = "auth-error";
    errorEl.style.display = "none";
    box.appendChild(errorEl);

    // Login form
    const loginForm = createLoginForm(errorEl, onSuccess);
    box.appendChild(loginForm);

    // Register form (hidden initially)
    const registerForm = createRegisterForm(errorEl, onSuccess);
    registerForm.style.display = "none";
    box.appendChild(registerForm);

    // Toggle link
    const toggle = document.createElement("p");
    toggle.className = "auth-toggle";
    box.appendChild(toggle);

    function showLogin(): void {
        errorEl.style.display = "none";
        loginForm.style.display = "block";
        registerForm.style.display = "none";
        toggle.innerHTML = 'No account? <a href="#">Register</a>';
        toggle.querySelector("a")!.addEventListener("click", (e) => {
            e.preventDefault();
            showRegister();
        });
    }

    function showRegister(): void {
        errorEl.style.display = "none";
        loginForm.style.display = "none";
        registerForm.style.display = "block";
        toggle.innerHTML = 'Have an account? <a href="#">Log in</a>';
        toggle.querySelector("a")!.addEventListener("click", (e) => {
            e.preventDefault();
            showLogin();
        });
    }

    showLogin();

    wrapper.appendChild(box);
    container.appendChild(wrapper);
}

function createLoginForm(errorEl: HTMLElement, onSuccess: AuthCallback): HTMLFormElement {
    const form = document.createElement("form");

    const usernameInput = createInput("text", "Username");
    form.appendChild(usernameInput);

    const passwordInput = createInput("password", "Password");
    form.appendChild(passwordInput);

    const button = document.createElement("button");
    button.type = "submit";
    button.textContent = "Log In";
    form.appendChild(button);

    form.addEventListener("submit", async (e) => {
        e.preventDefault();
        errorEl.style.display = "none";
        button.disabled = true;
        button.textContent = "Logging in...";

        try {
            await login(usernameInput.value.trim().toLowerCase(), passwordInput.value);
            onSuccess();
        } catch (err) {
            errorEl.textContent = (err as Error).message;
            errorEl.style.display = "block";
            button.disabled = false;
            button.textContent = "Log In";
        }
    });

    return form;
}

function createRegisterForm(errorEl: HTMLElement, onSuccess: AuthCallback): HTMLFormElement {
    const form = document.createElement("form");

    const nameInput = createInput("text", "Username");
    form.appendChild(nameInput);

    const emailInput = createInput("email", "Email");
    emailInput.required = true;
    form.appendChild(emailInput);

    const passwordInput = createInput("password", "Password (8+ characters)");
    form.appendChild(passwordInput);

    const confirmInput = createInput("password", "Confirm password");
    form.appendChild(confirmInput);

    const button = document.createElement("button");
    button.type = "submit";
    button.textContent = "Register";
    form.appendChild(button);

    form.addEventListener("submit", async (e) => {
        e.preventDefault();
        errorEl.style.display = "none";

        if (passwordInput.value !== confirmInput.value) {
            errorEl.textContent = "Passwords do not match";
            errorEl.style.display = "block";
            return;
        }

        if (passwordInput.value.length < 8) {
            errorEl.textContent = "Password must be at least 8 characters";
            errorEl.style.display = "block";
            return;
        }

        const agentName = nameInput.value.trim().toLowerCase();
        if (!/^[a-z][a-z0-9_-]{1,30}$/.test(agentName)) {
            errorEl.textContent = "Username must start with a letter, 2-31 chars, lowercase letters/numbers/hyphens only";
            errorEl.style.display = "block";
            return;
        }

        button.disabled = true;
        button.textContent = "Registering...";

        try {
            await register(agentName, passwordInput.value, emailInput.value.trim());
            onSuccess();
        } catch (err) {
            errorEl.textContent = (err as Error).message;
            errorEl.style.display = "block";
            button.disabled = false;
            button.textContent = "Register";
        }
    });

    return form;
}

function createInput(type: string, placeholder: string): HTMLInputElement {
    const input = document.createElement("input");
    input.type = type;
    input.placeholder = placeholder;
    input.required = true;
    return input;
}
