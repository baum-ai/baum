import { app } from "electron";

app.whenReady().then(() => {
  app.quit();
});
