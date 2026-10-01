const { api, shortID, setMessage, withBusy } = window.LockFlow;

const geometryForm = document.querySelector("#geometry-form");
const observationForm = document.querySelector("#observation-form");
const queryForm = document.querySelector("#query-form");
const geometryMessage = document.querySelector("#geometry-message");
const observationMessage = document.querySelector("#observation-message");
const queryMessage = document.querySelector("#query-message");
const accountsBody = document.querySelector("#accounts");

const fmt = (value, digits = 2) =>
  value === null || value === undefined
    ? "—"
    : Number(value).toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: digits });

function confidenceClass(confidence) {
  if (confidence === "low") return "critical";
  if (confidence === "medium") return "warning";
  return "";
}

function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (ch) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch]));
}

function buildQuery() {
  const chamber = document.querySelector("#q-chamber").value;
  const params = new URLSearchParams();
  const from = document.querySelector("#q-from").value.trim();
  const to = document.querySelector("#q-to").value.trim();
  if (from) params.set("from", from);
  if (to) params.set("to", to);
  const suffix = params.toString() ? `?${params.toString()}` : "";
  if (chamber) {
    return `/api/chambers/${encodeURIComponent(chamber)}/water/accounts${suffix}`;
  }
  return `/api/water/accounts${suffix}`;
}

function renderTrace(account) {
  return (account.quality.trace || []).map((ref) => {
    const device = (account.valve_trace || []).find((step) => step.command_id === ref.id);
    let detail = "";
    if (ref.kind === "valve.command" && device) {
      detail = ` — ${escapeHTML(device.device)} opening=${fmt(device.opening, 3)} (control position, not flow)`;
    } else if (ref.observed_at) {
      detail = ` — ${new Date(ref.observed_at).toLocaleString()}`;
    }
    return `<li>${escapeHTML(ref.kind)} · seq ${ref.sequence || "-"} · id ${shortID(ref.id)}${detail}</li>`;
  }).join("");
}

async function refresh() {
  setMessage(queryMessage, "");
  const accounts = await api(buildQuery());
  if (!accounts.length) {
    accountsBody.innerHTML = '<tr><td colspan="8" class="muted">No transits in this period</td></tr>';
    return;
  }
  accountsBody.innerHTML = accounts.map((account) => {
    const accountable = account.status === "accountable";
    const notes = (account.quality.notes || []).map((note) => `<li>${escapeHTML(note)}</li>`).join("");
    const traceCount = (account.quality.trace || []).length;
    const volume = accountable
      ? `${fmt(account.estimated_volume_m3, 1)} m³`
      : '<span class="status warning">not accountable</span>';
    return `<tr>
      <td>${escapeHTML(account.chamber_id)}</td>
      <td><a href="#" data-cycle="${account.cycle_id}" data-chamber="${account.chamber_id}" class="cycle-link">${shortID(account.cycle_id)}</a>
          <div class="muted">gen ${account.generation} · ${escapeHTML(account.stage || "-")}</div></td>
      <td>${escapeHTML(account.direction)}</td>
      <td><span class="status ${accountable ? "" : "warning"}">${account.status}</span></td>
      <td>${accountable ? fmt(account.level_change_m, 3) : "—"}</td>
      <td>${volume}</td>
      <td><span class="status ${confidenceClass(account.quality.confidence)}">${escapeHTML(account.quality.confidence)}</span></td>
      <td><ul style="margin:0;padding-left:16px">${notes}</ul>
          <details><summary>${traceCount} trace reference(s) → cycle ${shortID(account.cycle_id)}</summary>
          <ul style="margin:6px 0 0;padding-left:16px">${renderTrace(account)}</ul></details></td>
    </tr>`;
  }).join("");
}

accountsBody.addEventListener("click", async (event) => {
  const link = event.target.closest(".cycle-link");
  if (!link) return;
  event.preventDefault();
  try {
    const account = await api(`/api/chambers/${encodeURIComponent(link.dataset.chamber)}/water/accounts/${link.dataset.cycle}`);
    const lines = [
      `Chamber: ${account.chamber_id}`,
      `Cycle: ${account.cycle_id} (generation ${account.generation})`,
      `Direction: ${account.direction}, stage: ${account.stage}`,
      `Status: ${account.status}`,
      account.estimated_volume_m3 !== undefined && account.estimated_volume_m3 !== null
        ? `Estimated volume: ${account.estimated_volume_m3} m3 (level change ${account.level_change_m} m)`
        : "Estimated volume: not accountable",
      "",
      "Data quality:",
      ...(account.quality.notes || []),
    ];
    alert(lines.join("\n"));
  } catch (error) {
    setMessage(queryMessage, error.message);
  }
});

geometryForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  await withBusy(geometryForm.querySelector("button"), async () => {
    try {
      await api(`/api/chambers/${encodeURIComponent(document.querySelector("#g-chamber").value)}/water/geometry`, {
        method: "PUT",
        body: JSON.stringify({
          length_m: Number(document.querySelector("#g-length").value),
          width_m: Number(document.querySelector("#g-width").value),
          sill_elevation_m: Number(document.querySelector("#g-sill").value),
          deck_elevation_m: Number(document.querySelector("#g-deck").value),
        }),
      });
      setMessage(geometryMessage, "Geometry saved.");
      await refresh();
    } catch (error) {
      setMessage(geometryMessage, error.message);
    }
  });
});

observationForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  await withBusy(observationForm.querySelector("button"), async () => {
    try {
      const observedAt = document.querySelector("#o-time").value.trim();
      await api(`/api/chambers/${encodeURIComponent(document.querySelector("#o-chamber").value)}/water/observations`, {
        method: "POST",
        body: JSON.stringify({
          cycle_id: document.querySelector("#o-cycle").value.trim(),
          generation: Number(document.querySelector("#o-gen").value),
          boundary: document.querySelector("#o-boundary").value,
          meters: Number(document.querySelector("#o-level").value),
          ...(observedAt ? { observed_at: observedAt } : {}),
        }),
      });
      setMessage(observationMessage, "Observation recorded.");
      await refresh();
    } catch (error) {
      setMessage(observationMessage, error.message);
    }
  });
});

queryForm.addEventListener("submit", (event) => {
  event.preventDefault();
  refresh().catch((error) => setMessage(queryMessage, error.message));
});
document.querySelector("#refresh").addEventListener("click", () => {
  refresh().catch((error) => setMessage(queryMessage, error.message));
});

refresh().catch((error) => setMessage(queryMessage, error.message));
