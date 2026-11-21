// Minimal typing of the Express surface used by webhook-express.ts, so the example
// type-checks without installing express. Real projects use @types/express.
declare const express: {
  (): {
    post(path: string, ...handlers: Array<(req: any, res: any) => void>): void;
    use(handler: unknown): void;
    listen(port: number): void;
  };
  raw(opts: { type: string }): (req: any, res: any) => void;
  json(): unknown;
};
export default express;
