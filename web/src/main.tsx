import { render } from "preact";
import { App } from "./app";
import { startPersistence } from "./prefs";
import { connect, startSupervisor, onAlert } from "./stream";
import { startRouter } from "./router";
import { installNotifications, showAlert } from "./notify";

startPersistence();
startRouter();
installNotifications();
onAlert(showAlert);
connect();
startSupervisor();

// Suppress the colour transition for the first moment after boot, so applying a
// stored theme does not animate every cell.
document.documentElement.classList.add("no-transition");
setTimeout(() => document.documentElement.classList.remove("no-transition"), 200);

const root = document.getElementById("app");
if (root) render(<App />, root);
