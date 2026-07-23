
package main

import (
    "github.com/operatorstack/pitot/sdk"
    "github.com/operatorstack/pitot/schema"
)

func main() {
    sdk.RunController("test-controller", func(req schema.ControlRequested) sdk.Outcome {
        return sdk.Allow("test approved")
    })
}
