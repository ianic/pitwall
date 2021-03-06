package cmd

import (
	"strings"

	"github.com/minus5/pitwall/monit"
	"github.com/spf13/cobra"
)

var tailCmd = &cobra.Command{
	Use:   "tail",
	Short: "tail logs in datacenter <dc> for <service>",
	Long: `Tail logs in datacenter <dc> for <service>.
  If services is missing it will list all available services in <dc>.

  Examples:
    monit tail haproxy
    monit tail --dc pg1 haproxy
    monit tail backend_api -i request_logger -t url,method
    monit tail backend_api -i request_logger -a duration,status,code,lib
    monit tail backend_api -a listic -e request_logger.go:30`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) > 1 {
			cmd.Usage()
			return
		}
		service := ""
		if len(args) == 1 {
			service = args[0]
		}

		monit.Tail(monit.TailOptions{
			Address: getServiceAddress("nsq_notifier", "nsq-notifier"),
			Service: service,
			Json:    json,
			Pretty:  pretty,
			Exclude: splitComma(exclude),
			Include: splitComma(include),
		})

	},
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}

var (
	json    bool
	pretty  bool
	exclude string
	include string
)

func init() {
	rootCmd.AddCommand(tailCmd)
	//monitCmd.AddCommand(tailCmd)

	tailCmd.Flags().StringVarP(&dc, "dc", "d", "", "datacenter to find service")
	tailCmd.MarkFlagRequired("dc")

	tailCmd.Flags().BoolVarP(&json, "json", "j", false, "print unparsed json log line")
	tailCmd.Flags().BoolVarP(&pretty, "pretty", "p", false, "pretrty print json log line")
	tailCmd.Flags().StringVarP(&exclude, "exclude", "x", "", "list of attributes to EXCLUDE separated by ,")
	tailCmd.Flags().StringVarP(&include, "include", "i", "", "list of attributes to INCLUDE separated by ,")

}
