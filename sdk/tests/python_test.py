
import sys
from pitot.runner import run_controller, allow
from pitot.types import ControlRequested

def handler(req: ControlRequested):
    return allow("test approved")

if __name__ == "__main__":
    run_controller("test-controller", handler)
