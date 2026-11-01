// jsdom has no HTMLDialogElement.close; the tests' own showModal stubs only
// set `open`, so closing the dialog does the same.
if (typeof HTMLDialogElement !== "undefined" && !HTMLDialogElement.prototype.close) {
  HTMLDialogElement.prototype.close = function () {
    this.open = false;
  };
}
