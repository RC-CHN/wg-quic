// A read started before a completed mutation cannot replace its result.
export class ObservationClock {
  private revision = 0;
  capture(): number { return this.revision; }
  invalidate(): void { this.revision++; }
  accepts(revision: number): boolean { return revision === this.revision; }
}
