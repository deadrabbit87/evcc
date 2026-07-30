package sponsor

// LICENSE

// Copyright (c) evcc.io (andig, naltatis, premultiply)

// This module is NOT covered by the MIT license. All rights reserved.

// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

import (
	"context"
	"net"
	"time"

	"github.com/evcc-io/evcc/api/proto/pb"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/cloud"
	"github.com/evcc-io/evcc/util/request"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var hardwareLog = util.NewLogger("sponsor-hw")

// checkHardware registers the device with the sponsor server and checks authorization.
//
// DEBUG BUILD: this temporarily retries the check for up to ~30s to determine whether
// the sponsor server call fails because DNS/network isn't ready yet at process start
// (Andi's hypothesis), and logs the full request/response so a failed IsAuthorizedHardware
// call can be correlated against server-side records. Not for merge - production code
// does a single attempt and does not log request/response payloads.
func checkHardware(vendor string, metadata map[string]string) string {
	const maxAttempts = 7

	id := machineID()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		dnsStart := time.Now()
		addrs, dnsErr := net.DefaultResolver.LookupHost(context.Background(), "sponsor.evcc.io")
		hardwareLog.DEBUG.Printf("[DEBUG-DNS] attempt %d/%d: LookupHost(sponsor.evcc.io) took %s, addrs=%v err=%v",
			attempt, maxAttempts, time.Since(dnsStart), addrs, dnsErr)

		conn, err := cloud.Connection()
		if err != nil {
			hardwareLog.ERROR.Printf("[DEBUG-DNS] attempt %d/%d: cloud.Connection failed: %v", attempt, maxAttempts, err)
			time.Sleep(5 * time.Second)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), request.Timeout)
		client := pb.NewAuthClient(conn)

		hardwareLog.DEBUG.Printf("[DEBUG-DNS] attempt %d/%d: calling IsAuthorizedHardware, machineId=%q vendor=%q metadata=%v",
			attempt, maxAttempts, id, vendor, metadata)

		callStart := time.Now()
		res, err := client.IsAuthorizedHardware(ctx, &pb.HardwareRequest{
			MachineId: id,
			Vendor:    vendor,
			Metadata:  metadata,
		})
		cancel()

		if err != nil {
			hardwareLog.ERROR.Printf("[DEBUG-DNS] attempt %d/%d: IsAuthorizedHardware FAILED after %s: err=%v",
				attempt, maxAttempts, time.Since(callStart), err)
		} else {
			hardwareLog.DEBUG.Printf("[DEBUG-DNS] attempt %d/%d: IsAuthorizedHardware returned after %s: authorized=%v subject=%q",
				attempt, maxAttempts, time.Since(callStart), res.Authorized, res.Subject)
		}

		if err == nil && res.Authorized {
			hardwareLog.DEBUG.Printf("[DEBUG-DNS] attempt %d/%d: SUCCESS subject=%s (vendor=%s)", attempt, maxAttempts, res.Subject, vendor)
			return res.Subject
		}

		if s, ok := status.FromError(err); ok {
			hardwareLog.ERROR.Printf("[DEBUG-DNS] attempt %d/%d: grpc status code=%s msg=%q details=%v (vendor=%s)",
				attempt, maxAttempts, s.Code(), s.Message(), s.Details(), vendor)

			if s.Code() != codes.Unknown {
				if attempt == maxAttempts {
					hardwareLog.ERROR.Printf("[DEBUG-DNS] giving up after %d attempts, treating as unavailable", maxAttempts)
					return unavailable
				}
				time.Sleep(5 * time.Second)
				continue
			}
		}

		hardwareLog.DEBUG.Printf("[DEBUG-DNS] attempt %d/%d: not authorized, no retry (vendor=%s)", attempt, maxAttempts, vendor)
		return ""
	}

	return unavailable
}
