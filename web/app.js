(() => {
  "use strict";

  const state = {
    countries: [],
    selected: new Set(),
    loaded: false,
    loadingCountries: false,
    filter: "",
    generating: false,
  };

  const elements = {
    form: document.getElementById("builder-form"),
    listName: document.getElementById("list-name"),
    replaceExisting: document.getElementById("replace-existing"),
    search: document.getElementById("country-search"),
    countryGrid: document.getElementById("country-grid"),
    noResults: document.getElementById("no-results"),
    selectVisible: document.getElementById("select-visible"),
    clearSelection: document.getElementById("clear-selection"),
    selectionCount: document.getElementById("selection-count"),
    buildDetail: document.getElementById("build-detail"),
    generateButton: document.getElementById("generate-button"),
    formError: document.getElementById("form-error"),
    statusCard: document.querySelector(".database-card"),
    statusTitle: document.getElementById("status-title"),
    statusMessage: document.getElementById("status-message"),
    databaseMeta: document.getElementById("database-meta"),
  };

  function escapeText(value) {
    return String(value);
  }

  function countryCountLabel(count) {
    return `${count.toLocaleString()} ${count === 1 ? "country" : "countries"}`;
  }

  function filteredCountries() {
    const query = state.filter.trim().toLocaleLowerCase();
    if (!query) {
      return state.countries;
    }
    return state.countries.filter((country) => {
      return country.name.toLocaleLowerCase().includes(query)
        || country.code.toLocaleLowerCase().includes(query);
    });
  }

  function renderCountries() {
    const fragment = document.createDocumentFragment();
    const visible = new Set(filteredCountries().map((country) => country.code));

    for (const country of state.countries) {
      const label = document.createElement("label");
      label.className = "country-option";
      label.dataset.code = country.code;
      if (!visible.has(country.code)) {
        label.classList.add("is-hidden");
      }

      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.value = country.code;
      checkbox.checked = state.selected.has(country.code);
      checkbox.setAttribute("aria-label", `${escapeText(country.name)} (${country.code})`);
      checkbox.addEventListener("change", () => {
        if (checkbox.checked) {
          state.selected.add(country.code);
        } else {
          state.selected.delete(country.code);
        }
        updateSelectionUI();
      });

      const name = document.createElement("span");
      name.className = "country-name";
      name.textContent = country.name;

      const code = document.createElement("span");
      code.className = "country-code";
      code.textContent = country.code;

      label.append(checkbox, name, code);
      fragment.append(label);
    }

    elements.countryGrid.replaceChildren(fragment);
    elements.countryGrid.hidden = state.countries.length === 0;
    elements.noResults.hidden = filteredCountries().length !== 0 || state.countries.length === 0;
    updateSelectionUI();
  }

  function updateSelectionUI() {
    const count = state.selected.size;
    elements.selectionCount.textContent = countryCountLabel(count);
    elements.generateButton.disabled = count === 0 || !state.loaded || state.generating;
    elements.buildDetail.textContent = count === 0
      ? "Select at least one country."
      : `${countryCountLabel(count)} will be included in the script.`;

    const visible = filteredCountries();
    elements.selectVisible.disabled = visible.length === 0;
    elements.clearSelection.disabled = count === 0;
    elements.selectVisible.textContent = state.filter.trim() ? "Select matching" : "Select all";
  }

  function setStatus(status) {
    const current = status || {};
    const displayState = current.state || "loading";
    const titles = {
      ready: current.warning ? "Database ready · refresh issue" : "Database ready",
      loading: "Loading database",
      downloading: "Downloading database",
      updating: "Updating database",
      error: "Database unavailable",
    };

    elements.statusCard.dataset.state = displayState;
    elements.statusTitle.textContent = titles[displayState] || "Checking database";
    elements.statusMessage.textContent = current.message || "Checking the local database.";

    if (current.has_data) {
      const parts = [];
      if (current.database_version) {
        parts.push(`Version ${current.database_version}`);
      }
      if (current.range_count) {
        parts.push(`${current.range_count.toLocaleString()} ranges`);
      }
      if (current.file_size) {
        parts.push(formatBytes(current.file_size));
      }
      elements.databaseMeta.textContent = parts.join(" · ");
    } else {
      elements.databaseMeta.textContent = "The database is downloaded once per day.";
    }
  }

  function formatBytes(bytes) {
    const units = ["B", "KB", "MB", "GB"];
    let value = bytes;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) {
      value /= 1024;
      unit += 1;
    }
    return `${value.toFixed(unit > 1 ? 1 : 0)} ${units[unit]}`;
  }

  async function readJSON(response) {
    try {
      return await response.json();
    } catch {
      return {};
    }
  }

  async function loadCountries(force = false) {
    if (state.loadingCountries || (state.loaded && !force)) {
      return;
    }
    state.loadingCountries = true;

    try {
      const response = await fetch("/api/countries", { headers: { Accept: "application/json" } });
      const payload = await readJSON(response);
      if (!response.ok) {
        setStatus(payload.database);
        return;
      }

      state.countries = Array.isArray(payload.countries) ? payload.countries : [];
      state.loaded = state.countries.length > 0;
      setStatus(payload.database);
      if (state.loaded) {
        renderCountries();
      }
    } catch (error) {
      setStatus({
        state: "error",
        message: "The service could not be reached. Retrying shortly.",
      });
    } finally {
      state.loadingCountries = false;
    }
  }

  async function pollStatus() {
    let delay = 30000;
    try {
      const response = await fetch("/api/status", { headers: { Accept: "application/json" } });
      const status = await readJSON(response);
      setStatus(status);
      if (status.has_data && !state.loaded) {
        loadCountries();
      }
      if (["loading", "downloading", "updating"].includes(status.state)) {
        delay = 3000;
      }
    } catch {
      setStatus({
        state: "error",
        message: "The service could not be reached. Retrying shortly.",
      });
      delay = 5000;
    }
    window.setTimeout(pollStatus, delay);
  }

  function validateForm() {
    const name = elements.listName.value.trim();
    elements.listName.value = name;
    elements.listName.setAttribute("aria-invalid", "false");

    if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(name)) {
      elements.listName.setAttribute("aria-invalid", "true");
      throw new Error("Use 1–64 letters, numbers, dots, underscores, or hyphens for the list name.");
    }
    if (!state.loaded) {
      throw new Error("Wait for the country database to become available.");
    }
    if (state.selected.size === 0) {
      throw new Error("Select at least one country before generating the script.");
    }
    return name;
  }

  async function generateScript(event) {
    event.preventDefault();
    if (state.generating) {
      return;
    }

    elements.formError.textContent = "";
    let name;
    try {
      name = validateForm();
    } catch (error) {
      elements.formError.textContent = error.message;
      return;
    }

    const parameters = new URLSearchParams();
    parameters.set("name", name);
    parameters.set("replace", elements.replaceExisting.checked ? "true" : "false");
    for (const code of state.selected) {
      parameters.append("country", code);
    }

    state.generating = true;
    elements.generateButton.classList.add("is-loading");
    elements.generateButton.querySelector("span").textContent = "Generating…";
    updateSelectionUI();

    try {
      const response = await fetch(`/api/script?${parameters.toString()}`);
      if (!response.ok) {
        const payload = await readJSON(response);
        throw new Error(payload.error || "The script could not be generated.");
      }

      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = `${name}.rsc`;
      document.body.append(link);
      link.click();
      link.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (error) {
      elements.formError.textContent = error.message;
    } finally {
      state.generating = false;
      elements.generateButton.classList.remove("is-loading");
      elements.generateButton.querySelector("span").textContent = "Generate RouterOS script";
      updateSelectionUI();
    }
  }

  elements.search.addEventListener("input", () => {
    state.filter = elements.search.value;
    renderCountries();
  });

  elements.selectVisible.addEventListener("click", () => {
    for (const country of filteredCountries()) {
      state.selected.add(country.code);
    }
    renderCountries();
  });

  elements.clearSelection.addEventListener("click", () => {
    state.selected.clear();
    renderCountries();
  });

  elements.listName.addEventListener("input", () => {
    elements.listName.setAttribute("aria-invalid", "false");
    elements.formError.textContent = "";
  });

  elements.form.addEventListener("submit", generateScript);

  loadCountries();
  pollStatus();
})();
