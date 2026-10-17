import { Button } from "@/components/ui/button";
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Fieldset } from "@/components/ui/fieldset";
import { Input } from "@/components/ui/input";
import { Failure } from "@/features/mcp/shared";
import type { Connector } from "./directory";

/**
 * Setup for a server that takes an API key instead of browser sign-in (Similarweb). The key is
 * handed to the daemon's credential store and is not shown again.
 */
export function ApiKeyConnectorDialog({ connector, busy, error, onClose, onAdd }: {
  connector: Connector | null;
  busy: boolean;
  error: string | null;
  onClose: () => void;
  onAdd: (connector: Connector, key: string) => void;
}) {
  return (
    <Dialog open={connector !== null} onOpenChange={(next) => { if (!next && !busy) onClose(); }}>
      <DialogPopup anchored showCloseButton={!busy}>
        {connector?.apiKey && (
          <>
            <DialogHeader>
              <DialogTitle>Connect {connector.name}</DialogTitle>
              <DialogDescription>{connector.apiKey.help}</DialogDescription>
            </DialogHeader>
            <form className="contents" onSubmit={(event) => {
              event.preventDefault();
              onAdd(connector, String(new FormData(event.currentTarget).get("apiKey")).trim());
            }}>
              <DialogPanel>
                <Fieldset className="flex flex-col gap-4" disabled={busy}>
                  <Field>
                    <FieldLabel htmlFor="api-key">API key</FieldLabel>
                    <Input id="api-key" name="apiKey" type="password" required autoComplete="off" autoFocus />
                    <FieldDescription>Stored in Windows Credential Manager and never shown again.</FieldDescription>
                  </Field>
                  <Failure error={error} />
                </Fieldset>
              </DialogPanel>
              <DialogFooter>
                <Button type="button" variant="outline" disabled={busy} onClick={onClose}>Cancel</Button>
                <Button type="submit" disabled={busy}>{busy ? "Connecting…" : "Connect"}</Button>
              </DialogFooter>
            </form>
          </>
        )}
      </DialogPopup>
    </Dialog>
  );
}
