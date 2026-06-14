import { parseEventLines } from "./events";

self.onmessage = (event: MessageEvent<string[]>) => {
  self.postMessage(parseEventLines(event.data));
};
