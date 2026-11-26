# frozen_string_literal: true

# The types themselves are generated, in types_gen.rb. What the generator cannot say stays here.
module Hansho
  # How loudly a message is presented, lowest to highest (Level::VALUES).
  LEVELS = Level::VALUES

  # The #status of a Recipient outside the channel's audience: it got no message.
  RECIPIENT_NO_ACCESS = "no_access"

  # A delivery that did not fail has no #error: the hub's empty string reads as nil.
  Delivery.singleton_class.prepend(Module.new do
    def from_wire(raw)
      super.tap { |delivery| delivery.error = nil if delivery.error == "" }
    end
  end)
end
