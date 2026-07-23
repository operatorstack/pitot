
import { runController, allow } from '../typescript/src/runner';
import { ControlRequested } from '../typescript/src/pitot';

runController("test-controller", async (req: ControlRequested) => {
    return allow("test approved");
});
